/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

// Package afdgateway reconciles the POC Gateway API resources into Azure Front Door.
package afdgateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/annotations"
	"go.goms.io/fleet-networking/pkg/controllers/hub/gatewaymodel"
	"go.goms.io/fleet-networking/pkg/controllers/hub/multiclusterbackend"
	"go.goms.io/fleet-networking/pkg/providers/azure/frontdoor"
)

const (
	// ControllerName is the Gateway API controller name.
	ControllerName gatewayv1.GatewayController = "networking.fleet.azure.com/afd"
	// GatewayClassName is the sole GatewayClass supported by this POC.
	GatewayClassName gatewayv1.ObjectName = "azure-fleet-afd"
	// GatewayFinalizer protects a Gateway only after AFD ownership begins.
	GatewayFinalizer         = "networking.fleet.azure.com/afd-gateway-cleanup"
	routeConditionProgrammed = "Programmed"
	requeueDelay             = 15 * time.Second
)

// Provider is the fakeable AFD operation surface used by the controller.
type Provider interface {
	Reconcile(context.Context, gatewaymodel.GlobalGateway) (frontdoor.Result, error)
	Delete(context.Context, gatewaymodel.GlobalGateway) error
	WithdrawOrigins(context.Context, gatewaymodel.GlobalGateway, string, []string) error
}

// Reconciler is the single owner of Gateway, listener, and HTTPRoute parent status for this controller.
type Reconciler struct {
	client.Client
	Provider Provider
}

// ClassReconciler publishes acceptance for the one POC GatewayClass.
type ClassReconciler struct {
	client.Client
}

type routeValidationError struct{ message string }

func (e *routeValidationError) Error() string { return e.message }

func invalidRoute(message string, args ...any) error {
	return &routeValidationError{message: fmt.Sprintf(message, args...)}
}

// Reconcile publishes GatewayClass acceptance without adopting other classes.
func (r *ClassReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if req.Name != string(GatewayClassName) {
		return ctrl.Result{}, nil
	}
	var class gatewayv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	old := class.DeepCopy()
	status := metav1.ConditionFalse
	reason := string(gatewayv1.GatewayClassReasonInvalidParameters)
	message := fmt.Sprintf("spec.controllerName must be %q", ControllerName)
	if class.Spec.ControllerName == ControllerName {
		status = metav1.ConditionTrue
		reason = string(gatewayv1.GatewayClassReasonAccepted)
		message = "GatewayClass is accepted by the Fleet AFD controller"
	}
	meta.SetStatusCondition(&class.Status.Conditions, metav1.Condition{
		Type: string(gatewayv1.GatewayClassConditionStatusAccepted), Status: status,
		Reason: reason, Message: message, ObservedGeneration: class.Generation,
	})
	if equality.Semantic.DeepEqual(old.Status, class.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Patch(ctx, &class, client.MergeFrom(old))
}

// Reconcile validates one Gateway, resolves attached routes and backends, and publishes observed state.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if gateway.Spec.GatewayClassName != GatewayClassName {
		return ctrl.Result{}, nil
	}
	if !gateway.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &gateway)
	}

	config, err := validateGateway(&gateway)
	if err != nil {
		if statusErr := r.publishGatewayValidation(ctx, &gateway, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}
	if err := r.classAccepted(ctx); err != nil {
		if statusErr := r.publishGatewayValidation(ctx, &gateway, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, nil
	}

	routes, err := r.attachedRoutes(ctx, &gateway)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := gatewaymodel.GlobalGateway{
		Namespace: gateway.Namespace, Name: gateway.Name, UID: string(gateway.UID),
		WAFPolicyID: config.WAFPolicyID,
		Listeners: []gatewaymodel.Listener{{
			Name: string(gateway.Spec.Listeners[0].Name), Protocol: string(gateway.Spec.Listeners[0].Protocol),
			Port: int32(gateway.Spec.Listeners[0].Port),
		}},
	}
	allApproved := true
	routeErrors := make(map[types.NamespacedName]error)
	for i := range routes {
		routeModel, approved, routeErr := buildRoute(ctx, r.Client, &routes[i], gateway.Name)
		if routeErr != nil {
			routeErrors[client.ObjectKeyFromObject(&routes[i])] = routeErr
			allApproved = false
			continue
		}
		desired.Routes = append(desired.Routes, routeModel)
		allApproved = allApproved && approved
	}
	// Withdrawal must progress even when the removed member was the final ready
	// origin and the remaining route cannot yet be programmed.
	if err := r.withdrawTerminatingOrigins(ctx, desired); err != nil {
		return ctrl.Result{}, err
	}

	if len(routeErrors) != 0 || len(desired.Routes) == 0 {
		if err := r.publishPending(ctx, &gateway, routes, routeErrors, false, "references are not resolved"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Ownership begins only after the complete Kubernetes desired state is valid.
	if !containsString(gateway.Finalizers, GatewayFinalizer) {
		old := gateway.DeepCopy()
		gateway.Finalizers = append(gateway.Finalizers, GatewayFinalizer)
		if err := r.Patch(ctx, &gateway, client.MergeFrom(old)); err != nil {
			return ctrl.Result{}, fmt.Errorf("add Gateway finalizer: %w", err)
		}
	}

	result, err := r.Provider.Reconcile(ctx, desired)
	if err != nil {
		// Fail static: do not rewrite previously observed success on transient/provider errors.
		if !meta.IsStatusConditionTrue(gateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) {
			if statusErr := r.publishPending(ctx, &gateway, routes, nil, false, "Azure provider reconciliation failed"); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
		}
		return ctrl.Result{}, fmt.Errorf("reconcile Azure Front Door for Gateway %s/%s: %w", gateway.Namespace, gateway.Name, err)
	}
	programmed := result.Ready && allApproved
	message := "Azure Front Door configuration is ready"
	if !result.Ready {
		message = fmt.Sprintf("Azure Front Door provisioning is pending: %v", result.Pending)
	} else if !allApproved {
		message = "Private Link approval is pending for one or more origins"
	}
	if err := r.publishProgrammed(ctx, &gateway, routes, programmed, message, result.EndpointHostName); err != nil {
		return ctrl.Result{}, err
	}
	if !programmed {
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}
	return ctrl.Result{}, nil
}

func validateGateway(gateway *gatewayv1.Gateway) (annotations.GatewayConfig, error) {
	if gateway.Spec.GatewayClassName != GatewayClassName {
		return annotations.GatewayConfig{}, fmt.Errorf("unsupported GatewayClass %q", gateway.Spec.GatewayClassName)
	}
	if len(gateway.Spec.Addresses) != 0 {
		return annotations.GatewayConfig{}, errors.New("spec.addresses is not supported")
	}
	if len(gateway.Spec.Listeners) != 1 {
		return annotations.GatewayConfig{}, errors.New("exactly one HTTP listener is supported")
	}
	listener := gateway.Spec.Listeners[0]
	if listener.Protocol != gatewayv1.HTTPProtocolType || listener.Port != 80 || listener.TLS != nil {
		return annotations.GatewayConfig{}, errors.New("only an HTTP listener on port 80 without TLS is supported")
	}
	config, err := annotations.ParseGatewayConfig(gateway.Annotations, annotations.SKUPremium)
	if err != nil {
		return annotations.GatewayConfig{}, err
	}
	if config.SKU != annotations.SKUPremium {
		return annotations.GatewayConfig{}, fmt.Errorf("AFD SKU must be %q", annotations.SKUPremium)
	}
	if config.WAFPolicyID == "" {
		return annotations.GatewayConfig{}, errors.New("a readable pre-created WAF policy reference is required")
	}
	return config, nil
}

func (r *Reconciler) classAccepted(ctx context.Context) error {
	var class gatewayv1.GatewayClass
	if err := r.Get(ctx, types.NamespacedName{Name: string(GatewayClassName)}, &class); err != nil {
		return fmt.Errorf("get GatewayClass %q: %w", GatewayClassName, err)
	}
	if class.Spec.ControllerName != ControllerName {
		return fmt.Errorf("GatewayClass controllerName is %q, want %q", class.Spec.ControllerName, ControllerName)
	}
	return nil
}

func (r *Reconciler) attachedRoutes(ctx context.Context, gateway *gatewayv1.Gateway) ([]gatewayv1.HTTPRoute, error) {
	var list gatewayv1.HTTPRouteList
	if err := r.List(ctx, &list, client.InNamespace(gateway.Namespace)); err != nil {
		return nil, fmt.Errorf("list HTTPRoutes: %w", err)
	}
	result := make([]gatewayv1.HTTPRoute, 0, len(list.Items))
	for i := range list.Items {
		if routeAttaches(&list.Items[i], gateway.Name) {
			result = append(result, list.Items[i])
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func routeAttaches(route *gatewayv1.HTTPRoute, gatewayName string) bool {
	for _, parent := range route.Spec.ParentRefs {
		group := gatewayv1.GroupName
		if parent.Group != nil {
			group = string(*parent.Group)
		}
		kind := "Gateway"
		if parent.Kind != nil {
			kind = string(*parent.Kind)
		}
		if group == gatewayv1.GroupName && kind == "Gateway" && string(parent.Name) == gatewayName &&
			(parent.Namespace == nil || string(*parent.Namespace) == route.Namespace) {
			return true
		}
	}
	return false
}

func buildRoute(ctx context.Context, c client.Client, route *gatewayv1.HTTPRoute, gatewayName string) (gatewaymodel.Route, bool, error) {
	ref, err := validateRoute(route, gatewayName)
	if err != nil {
		return gatewaymodel.Route{}, false, err
	}
	var backend fleetnetv1alpha1.MultiClusterBackend
	key := types.NamespacedName{Namespace: route.Namespace, Name: string(ref.Name)}
	if err := c.Get(ctx, key, &backend); err != nil {
		return gatewaymodel.Route{}, false, fmt.Errorf("resolve MultiClusterBackend %s: %w", key, err)
	}
	var assignments fleetnetv1alpha1.ServiceOriginAssignmentList
	if err := c.List(ctx, &assignments); err != nil {
		return gatewaymodel.Route{}, false, fmt.Errorf("list ServiceOriginAssignments: %w", err)
	}
	active := make([]fleetnetv1alpha1.ServiceOriginAssignment, 0)
	allApproved := true
	for i := range assignments.Items {
		assignment := &assignments.Items[i]
		if !assignment.DeletionTimestamp.IsZero() ||
			assignment.Spec.BackendRef.Namespace != backend.Namespace ||
			assignment.Spec.BackendRef.Name != backend.Name ||
			assignment.Spec.BackendRef.UID != backend.UID {
			continue
		}
		active = append(active, *assignment)
		approved := assignment.Status.ObservedGeneration == assignment.Generation &&
			conditionCurrentTrue(assignment.Status.Conditions, string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady), assignment.Generation) &&
			conditionCurrentTrue(assignment.Status.Conditions, string(fleetnetv1alpha1.ServiceOriginAssignmentConditionPrivateLinkApproved), assignment.Generation)
		allApproved = allApproved && approved
	}
	normalized, err := gatewaymodel.BuildMultiClusterBackend(&backend, active)
	if err != nil {
		return gatewaymodel.Route{}, false, err
	}
	if len(normalized.Origins) == 0 {
		return gatewaymodel.Route{}, false, errors.New("backend has no current InfrastructureReady origin")
	}
	weight := int32(1)
	if ref.Weight != nil {
		weight = *ref.Weight
	}
	return gatewaymodel.Route{
		Namespace: route.Namespace, Name: route.Name,
		Matches: []gatewaymodel.HTTPMatch{{PathType: string(gatewayv1.PathMatchPathPrefix), Path: "/"}},
		Backends: []gatewaymodel.Backend{{
			Namespace: normalized.Namespace, Name: normalized.Name, Port: normalized.Port,
			RouteWeight: weight, HealthProbePath: normalized.HealthProbePath,
			OriginGroupName: normalized.OriginGroupName, Origins: normalized.Origins,
		}},
	}, allApproved, nil
}

func validateRoute(route *gatewayv1.HTTPRoute, gatewayName string) (gatewayv1.HTTPBackendRef, error) {
	if !routeAttaches(route, gatewayName) {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("route does not attach to this Gateway")
	}
	if len(route.Spec.Hostnames) != 0 || len(route.Spec.Rules) != 1 {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("only default-hostname routes with exactly one rule are supported")
	}
	rule := route.Spec.Rules[0]
	if len(rule.Filters) != 0 || len(rule.BackendRefs) != 1 || len(rule.Matches) != 1 {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("exactly one path match and one backend without filters are supported")
	}
	match := rule.Matches[0]
	if match.Method != nil || len(match.Headers) != 0 || len(match.QueryParams) != 0 || match.Path == nil ||
		match.Path.Type == nil || *match.Path.Type != gatewayv1.PathMatchPathPrefix ||
		match.Path.Value == nil || *match.Path.Value != "/" {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("only PathPrefix / without method, header, or query matches is supported")
	}
	ref := rule.BackendRefs[0]
	if ref.Port != nil {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("backendRef.port must be omitted")
	}
	group := ""
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := ""
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	if group != fleetnetv1alpha1.GroupVersion.Group || kind != fleetnetv1alpha1.MultiClusterBackendKind {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("backend kind must be %s/%s", fleetnetv1alpha1.GroupVersion.Group, fleetnetv1alpha1.MultiClusterBackendKind)
	}
	if ref.Namespace != nil && string(*ref.Namespace) != route.Namespace {
		return gatewayv1.HTTPBackendRef{}, invalidRoute("MultiClusterBackend must be in the route namespace")
	}
	return ref, nil
}

func (r *Reconciler) withdrawTerminatingOrigins(ctx context.Context, desired gatewaymodel.GlobalGateway) error {
	var assignments fleetnetv1alpha1.ServiceOriginAssignmentList
	if err := r.List(ctx, &assignments); err != nil {
		return fmt.Errorf("list terminating assignments: %w", err)
	}
	var backends fleetnetv1alpha1.MultiClusterBackendList
	if err := r.List(ctx, &backends); err != nil {
		return fmt.Errorf("list backends for withdrawal: %w", err)
	}
	for i := range backends.Items {
		backend := &backends.Items[i]
		terminating := make([]fleetnetv1alpha1.ServiceOriginAssignment, 0)
		for j := range assignments.Items {
			assignment := &assignments.Items[j]
			if assignment.DeletionTimestamp.IsZero() || assignment.Spec.BackendRef.UID != backend.UID ||
				!containsString(assignment.Finalizers, multiclusterbackend.OriginCleanupFinalizer) {
				continue
			}
			terminating = append(terminating, *assignment)
		}
		if len(terminating) == 0 {
			continue
		}
		model, err := gatewaymodel.BuildMultiClusterBackend(backend, terminating)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(model.Origins))
		for _, origin := range model.Origins {
			names = append(names, origin.Name)
		}
		if len(names) != 0 {
			if err := r.Provider.WithdrawOrigins(ctx, desired, model.OriginGroupName, names); err != nil {
				return fmt.Errorf("withdraw origins for backend %s/%s: %w", backend.Namespace, backend.Name, err)
			}
		}
		for j := range terminating {
			assignment := &terminating[j]
			old := assignment.DeepCopy()
			removeString(&assignment.Finalizers, multiclusterbackend.OriginCleanupFinalizer)
			if err := r.Patch(ctx, assignment, client.MergeFrom(old)); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("release assignment %s/%s after origin withdrawal: %w", assignment.Namespace, assignment.Name, err)
			}
		}
	}
	return nil
}

func (r *Reconciler) reconcileDelete(ctx context.Context, gateway *gatewayv1.Gateway) (reconcile.Result, error) {
	if !containsString(gateway.Finalizers, GatewayFinalizer) {
		return ctrl.Result{}, nil
	}
	desired := gatewaymodel.GlobalGateway{Namespace: gateway.Namespace, Name: gateway.Name, UID: string(gateway.UID)}
	if err := r.Provider.Delete(ctx, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete Azure Front Door for Gateway %s/%s: %w", gateway.Namespace, gateway.Name, err)
	}
	backendUIDs, err := r.backendUIDsForGateway(ctx, gateway)
	if err != nil {
		return ctrl.Result{}, err
	}
	var assignments fleetnetv1alpha1.ServiceOriginAssignmentList
	if err := r.List(ctx, &assignments); err != nil {
		return ctrl.Result{}, err
	}
	for i := range assignments.Items {
		assignment := &assignments.Items[i]
		if _, ownedBackend := backendUIDs[assignment.Spec.BackendRef.UID]; ownedBackend &&
			!assignment.DeletionTimestamp.IsZero() &&
			containsString(assignment.Finalizers, multiclusterbackend.OriginCleanupFinalizer) {
			old := assignment.DeepCopy()
			removeString(&assignment.Finalizers, multiclusterbackend.OriginCleanupFinalizer)
			if err := r.Patch(ctx, assignment, client.MergeFrom(old)); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
	}
	old := gateway.DeepCopy()
	removeString(&gateway.Finalizers, GatewayFinalizer)
	if err := r.Patch(ctx, gateway, client.MergeFrom(old)); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) backendUIDsForGateway(ctx context.Context, gateway *gatewayv1.Gateway) (map[types.UID]struct{}, error) {
	routes, err := r.attachedRoutes(ctx, gateway)
	if err != nil {
		return nil, err
	}
	result := make(map[types.UID]struct{})
	for i := range routes {
		for _, rule := range routes[i].Spec.Rules {
			for _, ref := range rule.BackendRefs {
				group := ""
				if ref.Group != nil {
					group = string(*ref.Group)
				}
				kind := ""
				if ref.Kind != nil {
					kind = string(*ref.Kind)
				}
				if group != fleetnetv1alpha1.GroupVersion.Group ||
					kind != fleetnetv1alpha1.MultiClusterBackendKind ||
					(ref.Namespace != nil && string(*ref.Namespace) != gateway.Namespace) {
					continue
				}
				var backend fleetnetv1alpha1.MultiClusterBackend
				if err := r.Get(ctx, types.NamespacedName{Namespace: gateway.Namespace, Name: string(ref.Name)}, &backend); err != nil {
					if apierrors.IsNotFound(err) {
						continue
					}
					return nil, fmt.Errorf("get backend during Gateway deletion: %w", err)
				}
				result[backend.UID] = struct{}{}
			}
		}
	}
	return result, nil
}

func (r *Reconciler) publishGatewayValidation(ctx context.Context, gateway *gatewayv1.Gateway, validationErr error) error {
	old := gateway.DeepCopy()
	setGatewayConditions(gateway, metav1.ConditionFalse, string(gatewayv1.GatewayReasonInvalid), validationErr.Error())
	setListeners(gateway, metav1.ConditionFalse, string(gatewayv1.ListenerReasonUnsupportedProtocol), validationErr.Error(), 0)
	return patchGatewayStatus(ctx, r.Client, gateway, old)
}

func (r *Reconciler) publishPending(
	ctx context.Context,
	gateway *gatewayv1.Gateway,
	routes []gatewayv1.HTTPRoute,
	routeErrors map[types.NamespacedName]error,
	programmed bool,
	message string,
) error {
	old := gateway.DeepCopy()
	setGatewayAccepted(gateway)
	status := metav1.ConditionFalse
	if programmed {
		status = metav1.ConditionTrue
	}
	setGatewayProgrammed(gateway, status, message)
	// Gateway API limits attached routes well below int32.
	setListeners(gateway, metav1.ConditionTrue, string(gatewayv1.ListenerReasonAccepted), "listener is accepted", int32(len(routes))) // #nosec G115
	if err := patchGatewayStatus(ctx, r.Client, gateway, old); err != nil {
		return err
	}
	for i := range routes {
		routeErr := routeErrors[client.ObjectKeyFromObject(&routes[i])]
		if err := r.patchRouteStatus(ctx, &routes[i], gateway, routeErr == nil, false, routeErr); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) publishProgrammed(
	ctx context.Context,
	gateway *gatewayv1.Gateway,
	routes []gatewayv1.HTTPRoute,
	programmed bool,
	message, endpointHostName string,
) error {
	old := gateway.DeepCopy()
	if endpointHostName != "" {
		addressType := gatewayv1.HostnameAddressType
		gateway.Status.Addresses = []gatewayv1.GatewayStatusAddress{{Type: &addressType, Value: endpointHostName}}
	}
	setGatewayAccepted(gateway)
	status := metav1.ConditionFalse
	if programmed {
		status = metav1.ConditionTrue
	}
	setGatewayProgrammed(gateway, status, message)
	// Gateway API limits attached routes well below int32.
	setListeners(gateway, status, conditionReason(programmed, string(gatewayv1.ListenerReasonProgrammed), string(gatewayv1.ListenerReasonPending)), message, int32(len(routes))) // #nosec G115
	if err := patchGatewayStatus(ctx, r.Client, gateway, old); err != nil {
		return err
	}
	for i := range routes {
		if err := r.patchRouteStatus(ctx, &routes[i], gateway, true, programmed, nil); err != nil {
			return err
		}
		if err := r.patchBackendProgrammed(ctx, &routes[i], programmed, message); err != nil {
			return err
		}
	}
	return nil
}

func setGatewayAccepted(gateway *gatewayv1.Gateway) {
	meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
		Type: string(gatewayv1.GatewayConditionAccepted), Status: metav1.ConditionTrue,
		Reason: string(gatewayv1.GatewayReasonAccepted), Message: "Gateway configuration is accepted",
		ObservedGeneration: gateway.Generation,
	})
}

func setGatewayConditions(gateway *gatewayv1.Gateway, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
		Type: string(gatewayv1.GatewayConditionAccepted), Status: status, Reason: reason,
		Message: message, ObservedGeneration: gateway.Generation,
	})
	setGatewayProgrammed(gateway, metav1.ConditionFalse, message)
}

func setGatewayProgrammed(gateway *gatewayv1.Gateway, status metav1.ConditionStatus, message string) {
	reason := string(gatewayv1.GatewayReasonPending)
	if status == metav1.ConditionTrue {
		reason = string(gatewayv1.GatewayReasonProgrammed)
	}
	meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
		Type: string(gatewayv1.GatewayConditionProgrammed), Status: status, Reason: reason,
		Message: message, ObservedGeneration: gateway.Generation,
	})
}

func setListeners(gateway *gatewayv1.Gateway, status metav1.ConditionStatus, reason, message string, attached int32) {
	gateway.Status.Listeners = make([]gatewayv1.ListenerStatus, 0, len(gateway.Spec.Listeners))
	for _, listener := range gateway.Spec.Listeners {
		conditions := []metav1.Condition{
			{Type: string(gatewayv1.ListenerConditionAccepted), Status: status, Reason: reason, Message: message, ObservedGeneration: gateway.Generation},
			{Type: string(gatewayv1.ListenerConditionProgrammed), Status: status, Reason: reason, Message: message, ObservedGeneration: gateway.Generation},
			{Type: string(gatewayv1.ListenerConditionResolvedRefs), Status: status, Reason: conditionReason(status == metav1.ConditionTrue, string(gatewayv1.ListenerReasonResolvedRefs), string(gatewayv1.ListenerReasonInvalidRouteKinds)), Message: message, ObservedGeneration: gateway.Generation},
		}
		gateway.Status.Listeners = append(gateway.Status.Listeners, gatewayv1.ListenerStatus{
			Name: listener.Name, SupportedKinds: []gatewayv1.RouteGroupKind{{Group: groupPtr(gatewayv1.GroupName), Kind: "HTTPRoute"}},
			AttachedRoutes: attached, Conditions: conditions,
		})
	}
}

func (r *Reconciler) patchRouteStatus(ctx context.Context, route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, resolved, programmed bool, routeErr error) error {
	var current gatewayv1.HTTPRoute
	if err := r.Get(ctx, client.ObjectKeyFromObject(route), &current); err != nil {
		return err
	}
	old := current.DeepCopy()
	parentRef := matchingParentRef(&current, gateway.Name)
	conditions := []metav1.Condition{
		{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayv1.RouteReasonAccepted), Message: "route is accepted", ObservedGeneration: current.Generation},
	}
	var validationErr *routeValidationError
	if errors.As(routeErr, &validationErr) {
		conditions[0] = metav1.Condition{
			Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionFalse,
			Reason: string(gatewayv1.RouteReasonUnsupportedValue), Message: routeErr.Error(),
			ObservedGeneration: current.Generation,
		}
	}
	resolvedStatus := metav1.ConditionTrue
	resolvedReason := string(gatewayv1.RouteReasonResolvedRefs)
	resolvedMessage := "backend references are resolved"
	if !resolved {
		resolvedStatus = metav1.ConditionFalse
		resolvedReason = string(gatewayv1.RouteReasonBackendNotFound)
		if routeErr != nil {
			resolvedMessage = routeErr.Error()
		}
	}
	conditions = append(conditions,
		metav1.Condition{Type: string(gatewayv1.RouteConditionResolvedRefs), Status: resolvedStatus, Reason: resolvedReason, Message: resolvedMessage, ObservedGeneration: current.Generation},
		metav1.Condition{Type: routeConditionProgrammed, Status: boolCondition(programmed), Reason: conditionReason(programmed, string(gatewayv1.GatewayReasonProgrammed), string(gatewayv1.RouteReasonPending)), Message: conditionMessage(programmed), ObservedGeneration: current.Generation},
	)
	parent := gatewayv1.RouteParentStatus{ParentRef: parentRef, ControllerName: ControllerName, Conditions: conditions}
	replaced := false
	for i := range current.Status.Parents {
		if current.Status.Parents[i].ControllerName == ControllerName && parentRefsEqual(current.Status.Parents[i].ParentRef, parentRef) {
			current.Status.Parents[i] = parent
			replaced = true
			break
		}
	}
	if !replaced {
		current.Status.Parents = append(current.Status.Parents, parent)
	}
	if equality.Semantic.DeepEqual(old.Status, current.Status) {
		return nil
	}
	return r.Status().Patch(ctx, &current, client.MergeFrom(old))
}

func (r *Reconciler) patchBackendProgrammed(ctx context.Context, route *gatewayv1.HTTPRoute, programmed bool, message string) error {
	ref := route.Spec.Rules[0].BackendRefs[0]
	var backend fleetnetv1alpha1.MultiClusterBackend
	if err := r.Get(ctx, types.NamespacedName{Namespace: route.Namespace, Name: string(ref.Name)}, &backend); err != nil {
		return client.IgnoreNotFound(err)
	}
	old := backend.DeepCopy()
	status := metav1.ConditionFalse
	reason := fleetnetv1alpha1.MultiClusterBackendReasonProgramming
	if programmed {
		status = metav1.ConditionTrue
		reason = fleetnetv1alpha1.MultiClusterBackendReasonProgrammed
	}
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type: string(fleetnetv1alpha1.MultiClusterBackendConditionProgrammed), Status: status,
		Reason: string(reason), Message: message, ObservedGeneration: backend.Generation,
	})
	if equality.Semantic.DeepEqual(old.Status, backend.Status) {
		return nil
	}
	return r.Status().Patch(ctx, &backend, client.MergeFrom(old))
}

func patchGatewayStatus(ctx context.Context, c client.Client, gateway, old *gatewayv1.Gateway) error {
	if equality.Semantic.DeepEqual(old.Status, gateway.Status) {
		return nil
	}
	return c.Status().Patch(ctx, gateway, client.MergeFrom(old))
}

func matchingParentRef(route *gatewayv1.HTTPRoute, gatewayName string) gatewayv1.ParentReference {
	for _, ref := range route.Spec.ParentRefs {
		if string(ref.Name) == gatewayName {
			return ref
		}
	}
	return gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayName)}
}

func parentRefsEqual(left, right gatewayv1.ParentReference) bool {
	return equality.Semantic.DeepEqual(left, right)
}

func groupPtr(value string) *gatewayv1.Group {
	group := gatewayv1.Group(value)
	return &group
}

func boolCondition(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func conditionCurrentTrue(conditions []metav1.Condition, conditionType string, generation int64) bool {
	condition := meta.FindStatusCondition(conditions, conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == generation
}

func conditionReason(ok bool, success, pending string) string {
	if ok {
		return success
	}
	return pending
}

func conditionMessage(programmed bool) string {
	if programmed {
		return "route is programmed"
	}
	return "route programming is pending"
}

func containsString(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func removeString(values *[]string, value string) {
	for i, current := range *values {
		if current == value {
			*values = append((*values)[:i], (*values)[i+1:]...)
			return
		}
	}
}

// SetupWithManager installs the GatewayClass and coherent Gateway/HTTPRoute orchestrator.
func SetupWithManager(mgr ctrl.Manager, provider Provider) error {
	if provider == nil {
		return errors.New("AFD provider must not be nil")
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("afd-gatewayclass-controller").
		For(&gatewayv1.GatewayClass{}).
		Complete(&ClassReconciler{Client: mgr.GetClient()}); err != nil {
		return err
	}
	r := &Reconciler{Client: mgr.GetClient(), Provider: provider}
	return ctrl.NewControllerManagedBy(mgr).
		Named("afd-gateway-controller").
		For(&gatewayv1.Gateway{}).
		Watches(&gatewayv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(r.requestsForRoute)).
		Watches(&fleetnetv1alpha1.MultiClusterBackend{}, handler.EnqueueRequestsFromMapFunc(r.requestsForBackend)).
		Watches(&fleetnetv1alpha1.ServiceOriginAssignment{}, handler.EnqueueRequestsFromMapFunc(r.requestsForAssignment)).
		Complete(r)
}

func (r *Reconciler) requestsForRoute(_ context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gatewayv1.HTTPRoute)
	if !ok {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(route.Spec.ParentRefs))
	for _, parent := range route.Spec.ParentRefs {
		if parent.Namespace != nil && string(*parent.Namespace) != route.Namespace {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: string(parent.Name)}})
	}
	return requests
}

func (r *Reconciler) requestsForBackend(ctx context.Context, obj client.Object) []reconcile.Request {
	backend, ok := obj.(*fleetnetv1alpha1.MultiClusterBackend)
	if !ok {
		return nil
	}
	return r.gatewaysForNamespace(ctx, backend.Namespace)
}

func (r *Reconciler) requestsForAssignment(ctx context.Context, obj client.Object) []reconcile.Request {
	assignment, ok := obj.(*fleetnetv1alpha1.ServiceOriginAssignment)
	if !ok {
		return nil
	}
	return r.gatewaysForNamespace(ctx, assignment.Spec.BackendRef.Namespace)
}

func (r *Reconciler) gatewaysForNamespace(ctx context.Context, namespace string) []reconcile.Request {
	var gateways gatewayv1.GatewayList
	if err := r.List(ctx, &gateways, client.InNamespace(namespace)); err != nil {
		klog.ErrorS(err, "Failed to list Gateways for dependency event", "namespace", namespace)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(gateways.Items))
	for i := range gateways.Items {
		if gateways.Items[i].Spec.GatewayClassName == GatewayClassName {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&gateways.Items[i])})
		}
	}
	return requests
}
