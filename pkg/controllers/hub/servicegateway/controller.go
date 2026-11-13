/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

// Package servicegateway implements the opt-in regional TCP Service Gateway path.
package servicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	fleetnet "go.goms.io/fleet-networking/api/v1alpha1"
	fleetbeta "go.goms.io/fleet-networking/api/v1beta1"
	"go.goms.io/fleet-networking/pkg/common/hubconfig"
	azure "go.goms.io/fleet-networking/pkg/providers/azure/servicegateway"
)

const finalizer = "networking.fleet.azure.com/service-gateway-cleanup"

type ResourceProvider interface {
	Prepare(context.Context, string, azure.Config) (azure.Journal, error)
	Reconcile(context.Context, string, azure.Journal, []azure.Application) (map[string]string, error)
	Withdraw(context.Context, string, azure.Journal) error
}

type Reconciler struct {
	Client    client.Client
	APIReader client.Reader
	Provider  ResourceProvider
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;gatewayclasses;tcproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;gateways/status;gateways/finalizers;gatewayclasses/status;tcproutes/status,verbs=patch;update
// +kubebuilder:rbac:groups=networking.fleet.azure.com,resources=serviceimports;internalserviceexports;endpointsliceexports,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gateway gatewayv1.Gateway
	if err := r.Client.Get(ctx, req.NamespacedName, &gateway); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var journal azure.Journal
	hasJournal := gateway.Annotations[azure.JournalAnnotation] != ""
	if hasJournal {
		if err := json.Unmarshal([]byte(gateway.Annotations[azure.JournalAnnotation]), &journal); err != nil {
			return ctrl.Result{}, fmt.Errorf("invalid rollback journal: %w", err)
		}
	}
	if gateway.DeletionTimestamp != nil && controllerutil.ContainsFinalizer(&gateway, finalizer) {
		if r.Provider == nil {
			return ctrl.Result{}, fmt.Errorf("Service Gateway provider is not configured")
		}
		if hasJournal {
			if err := r.Provider.Withdraw(ctx, string(gateway.UID), journal); err != nil {
				return r.result(ctx, &gateway, err)
			}
		}
		before := gateway.DeepCopy()
		controllerutil.RemoveFinalizer(&gateway, finalizer)
		return ctrl.Result{}, r.Client.Patch(ctx, &gateway, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}
	var class gatewayv1.GatewayClass
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(gateway.Spec.GatewayClassName)}, &class); err != nil {
		return ctrl.Result{}, err
	}
	if class.Spec.ControllerName != azure.ControllerName {
		return ctrl.Result{}, nil
	}
	if gateway.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	if r.Provider == nil {
		return ctrl.Result{}, fmt.Errorf("Service Gateway provider is not configured")
	}
	classBefore := class.DeepCopy()
	classStatus := metav1.ConditionTrue
	classReason := "Accepted"
	if class.Spec.ParametersRef != nil {
		classStatus = metav1.ConditionFalse
		classReason = "InvalidParameters"
	}
	meta.SetStatusCondition(&class.Status.Conditions, metav1.Condition{Type: "Accepted", Status: classStatus, Reason: classReason,
		Message: "Experimental Fleet TCP Service Gateway controller", ObservedGeneration: class.Generation})
	if !reflect.DeepEqual(classBefore.Status, class.Status) {
		if err := r.Client.Status().Patch(ctx, &class, client.MergeFromWithOptions(classBefore, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
	}
	config, err := azure.ParseConfig(gateway.Annotations[azure.ConfigAnnotation])
	if err != nil {
		return r.result(ctx, &gateway, err)
	}
	if class.Spec.ParametersRef != nil || len(gateway.Spec.Addresses) != 0 {
		return r.result(ctx, &gateway, fmt.Errorf("class parameters and supplied Gateway addresses are unsupported"))
	}
	if hasJournal && !reflect.DeepEqual(journal.Config, config) {
		return r.result(ctx, &gateway, fmt.Errorf("network configuration is immutable while enrolled; remove routes and await withdrawal first"))
	}
	apps, err := r.resolve(ctx, &gateway)
	if err != nil {
		return r.result(ctx, &gateway, err)
	}
	if !hasJournal && len(apps) == 0 {
		return r.result(ctx, &gateway, nil)
	}
	if !hasJournal {
		var retired []string
		if value := gateway.Annotations[azure.RetiredRootsAnnotation]; value != "" {
			if err := json.Unmarshal([]byte(value), &retired); err != nil {
				return r.result(ctx, &gateway, fmt.Errorf("invalid retired resource journal: %w", err))
			}
		}
		for _, id := range []string{config.GatewayID, config.NATGatewayID, config.EgressPublicIPID} {
			for _, old := range retired {
				if strings.EqualFold(id, old) {
					return r.result(ctx, &gateway, fmt.Errorf("resource ID %s was deleted by an earlier enrollment; configure fresh root resource IDs before re-enrolling", id))
				}
			}
		}
		journal, err = r.Provider.Prepare(ctx, string(gateway.UID), config)
		if err != nil {
			return r.result(ctx, &gateway, err)
		}
		data, err := json.Marshal(journal)
		if err != nil {
			return ctrl.Result{}, err
		}
		before := gateway.DeepCopy()
		if gateway.Annotations == nil {
			gateway.Annotations = map[string]string{}
		}
		gateway.Annotations[azure.JournalAnnotation] = string(data)
		controllerutil.AddFinalizer(&gateway, finalizer)
		if err := r.Client.Patch(ctx, &gateway, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	vips, err := r.Provider.Reconcile(ctx, string(gateway.UID), journal, apps)
	if err != nil {
		return r.result(ctx, &gateway, err)
	}
	if len(apps) == 0 {
		before := gateway.DeepCopy()
		var retired []string
		if value := gateway.Annotations[azure.RetiredRootsAnnotation]; value != "" {
			if err := json.Unmarshal([]byte(value), &retired); err != nil {
				return ctrl.Result{}, fmt.Errorf("invalid retired resource journal: %w", err)
			}
		}
		retired = append(retired, journal.Config.GatewayID, journal.Config.NATGatewayID, journal.Config.EgressPublicIPID)
		data, err := json.Marshal(retired)
		if err != nil {
			return ctrl.Result{}, err
		}
		gateway.Annotations[azure.RetiredRootsAnnotation] = string(data)
		delete(gateway.Annotations, azure.JournalAnnotation)
		controllerutil.RemoveFinalizer(&gateway, finalizer)
		if err := r.Client.Patch(ctx, &gateway, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
	}
	before := gateway.DeepCopy()
	gateway.Status.Addresses = nil
	var listenerStatuses []gatewayv1.ListenerStatus
	for _, listener := range gateway.Spec.Listeners {
		observed := gatewayv1.ListenerStatus{Name: listener.Name, SupportedKinds: []gatewayv1.RouteGroupKind{{Kind: "TCPRoute"}}}
		for _, previous := range gateway.Status.Listeners {
			if previous.Name == listener.Name {
				observed.Conditions = previous.Conditions
			}
		}
		programmed := false
		for _, app := range apps {
			if app.FrontendPort == int32(listener.Port) {
				observed.AttachedRoutes++
				programmed = len(app.Backends) > 0
			}
		}
		meta.SetStatusCondition(&observed.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue,
			Reason: "Accepted", Message: "Plain TCP listener accepted", ObservedGeneration: gateway.Generation})
		listenerStatus := metav1.ConditionFalse
		if programmed {
			listenerStatus = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&observed.Conditions, metav1.Condition{Type: "Programmed", Status: listenerStatus,
			Reason: "Reconciled", Message: "Observed owned Service LB and ready backend membership", ObservedGeneration: gateway.Generation})
		listenerStatuses = append(listenerStatuses, observed)
	}
	gateway.Status.Listeners = listenerStatuses
	for _, app := range apps {
		kind := gatewayv1.IPAddressType
		gateway.Status.Addresses = append(gateway.Status.Addresses, gatewayv1.GatewayStatusAddress{Type: &kind, Value: vips[app.Identity]})
	}
	ready := len(apps) > 0
	for _, app := range apps {
		ready = ready && len(app.Backends) > 0
	}
	meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", Message: "Experimental TCP graph accepted", ObservedGeneration: gateway.Generation})
	setStatus(&gateway, ready, "Reconciled", "Fleet-owned Service Gateway graph reconciled; no-backend applications are not ready")
	if err := r.Client.Status().Patch(ctx, &gateway, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func setStatus(g *gatewayv1.Gateway, programmed bool, reason, message string) {
	status := metav1.ConditionFalse
	if programmed {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&g.Status.Conditions, metav1.Condition{Type: "Programmed", Status: status, Reason: reason, Message: message, ObservedGeneration: g.Generation})
}

func (r *Reconciler) result(ctx context.Context, g *gatewayv1.Gateway, reconcileErr error) (ctrl.Result, error) {
	before := g.DeepCopy()
	message := "No opted-in ServiceImport backends; network is withdrawn"
	reason := "Pending"
	if reconcileErr != nil {
		message = reconcileErr.Error()
	}
	if reconcileErr != nil && !errors.Is(reconcileErr, azure.ErrPending) {
		reason = "Invalid"
		ctrl.LoggerFrom(ctx).Error(reconcileErr, "Service Gateway reconciliation failed")
	}
	setStatus(g, false, reason, message)
	if err := r.Client.Status().Patch(ctx, g, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, errors.Join(reconcileErr, err)
	}
	if errors.Is(reconcileErr, azure.ErrPending) {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, reconcileErr
}

func (r *Reconciler) resolve(ctx context.Context, g *gatewayv1.Gateway) ([]azure.Application, error) {
	listeners := map[gatewayv1.SectionName]gatewayv1.Listener{}
	for _, listener := range g.Spec.Listeners {
		if listener.Protocol != gatewayv1.TCPProtocolType || listener.Hostname != nil || listener.TLS != nil {
			return nil, fmt.Errorf("Service Gateway supports plain TCP listeners only")
		}
		if listener.AllowedRoutes != nil && listener.AllowedRoutes.Namespaces != nil &&
			listener.AllowedRoutes.Namespaces.From != nil && *listener.AllowedRoutes.Namespaces.From != gatewayv1.NamespacesFromSame {
			return nil, fmt.Errorf("only same-namespace TCPRoutes are supported")
		}
		if listener.AllowedRoutes != nil && len(listener.AllowedRoutes.Kinds) != 0 {
			allowed := false
			for _, kind := range listener.AllowedRoutes.Kinds {
				allowed = allowed || (kind.Kind == "TCPRoute" && (kind.Group == nil || *kind.Group == gatewayv1.GroupName))
			}
			if !allowed {
				return nil, fmt.Errorf("listener %s does not allow TCPRoute", listener.Name)
			}
		}
		listeners[listener.Name] = listener
	}
	var routes gatewayv1alpha2.TCPRouteList
	if err := r.APIReader.List(ctx, &routes, client.InNamespace(g.Namespace)); err != nil {
		return nil, err
	}
	var exports fleetnet.EndpointSliceExportList
	if err := r.APIReader.List(ctx, &exports); err != nil {
		return nil, err
	}
	var serviceExports fleetnet.InternalServiceExportList
	if err := r.APIReader.List(ctx, &serviceExports); err != nil {
		return nil, err
	}
	var apps []azure.Application
	ports := map[int32]bool{}
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.DeletionTimestamp != nil {
			continue
		}
		var attached *gatewayv1.Listener
		for _, parent := range route.Spec.ParentRefs {
			if string(parent.Name) != g.Name || parent.Namespace != nil && string(*parent.Namespace) != g.Namespace ||
				parent.Group != nil && *parent.Group != gatewayv1.GroupName || parent.Kind != nil && *parent.Kind != "Gateway" {
				continue
			}
			if parent.SectionName == nil {
				return nil, fmt.Errorf("TCPRoute %s requires parent sectionName", route.Name)
			}
			listener, ok := listeners[*parent.SectionName]
			if !ok || parent.Port != nil && *parent.Port != listener.Port {
				return nil, fmt.Errorf("TCPRoute %s references an unknown listener", route.Name)
			}
			if attached != nil {
				return nil, fmt.Errorf("multiple bindings of one route are unsupported")
			}
			attached = &listener
		}
		if attached == nil {
			continue
		}
		if len(route.Spec.Rules) != 1 || len(route.Spec.Rules[0].BackendRefs) != 1 {
			return nil, fmt.Errorf("TCPRoute %s requires one rule and one ServiceImport backend", route.Name)
		}
		ref := route.Spec.Rules[0].BackendRefs[0]
		if ref.Group == nil || string(*ref.Group) != fleetnet.GroupVersion.Group || ref.Kind == nil ||
			*ref.Kind != "ServiceImport" || ref.Namespace != nil && string(*ref.Namespace) != g.Namespace ||
			ref.Port == nil || ref.Weight != nil && *ref.Weight != 1 {
			return nil, fmt.Errorf("TCPRoute %s has unsupported backend reference", route.Name)
		}
		var service fleetnet.ServiceImport
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: g.Namespace, Name: string(ref.Name)}, &service); err != nil {
			if client.IgnoreNotFound(err) == nil {
				if err := r.routeStatus(ctx, route, g, false, "BackendNotFound", "Referenced ServiceImport does not exist"); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		if service.DeletionTimestamp != nil {
			continue
		}
		if ports[int32(attached.Port)] {
			return nil, fmt.Errorf("multiple applications on one listener port are unsupported")
		}
		ports[int32(attached.Port)] = true
		var selected *fleetnet.ServicePort
		for j := range service.Status.Ports {
			port := &service.Status.Ports[j]
			if port.Port == int32(*ref.Port) && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
				if selected != nil {
					return nil, fmt.Errorf("ambiguous service port")
				}
				selected = port
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("ServiceImport %s has no accepted TCP port", service.Name)
		}
		app := azure.Application{Identity: azure.Identity(string(g.UID), string(route.UID)+"/"+string(service.UID)), FrontendPort: int32(attached.Port)}
		for _, cluster := range service.Status.Clusters {
			namespace := fmt.Sprintf(hubconfig.HubNamespaceNameFormat, cluster.Cluster)
			valid := false
			acceptedUID := ""
			for _, export := range serviceExports.Items {
				source := export.Spec.ServiceReference
				if export.Namespace != namespace || source.ClusterID != cluster.Cluster || source.Namespace != service.Namespace ||
					source.Name != service.Name || export.DeletionTimestamp != nil {
					continue
				}
				condition := meta.FindStatusCondition(export.Status.Conditions, string(fleetbeta.ServiceExportConflict))
				if source.UID != "" && source.NamespacedName == service.Namespace+"/"+service.Name &&
					reflect.DeepEqual(export.Spec.Ports, service.Status.Ports) &&
					condition != nil && condition.Status == metav1.ConditionFalse &&
					condition.ObservedGeneration == export.Generation && export.Spec.Type == corev1.ServiceTypeClusterIP {
					if valid {
						return nil, fmt.Errorf("multiple accepted exports for member %s", cluster.Cluster)
					}
					valid = true
					acceptedUID = string(source.UID)
				}
			}
			if !valid {
				return nil, fmt.Errorf("member %s lacks an accepted ClusterIP export for %s", cluster.Cluster, service.Name)
			}
			var candidates []fleetnet.EndpointSliceExport
			for _, export := range exports.Items {
				if export.Namespace == namespace && export.Spec.EndpointSliceReference.ClusterID == cluster.Cluster &&
					export.Spec.OwnerServiceReference.Namespace == service.Namespace &&
					export.Spec.OwnerServiceReference.Name == service.Name {
					candidates = append(candidates, export)
				}
			}
			currentExports, err := currentEndpointExports(candidates)
			if err != nil {
				return nil, err
			}
			for _, export := range currentExports {
				source := export.Spec.EndpointSliceReference
				owner := export.Spec.OwnerServiceReference
				if export.Namespace != namespace || source.ClusterID != cluster.Cluster ||
					owner.Namespace != service.Namespace || owner.Name != service.Name || export.DeletionTimestamp != nil {
					continue
				}
				if acceptedUID == "" || source.UID == "" || source.Namespace != service.Namespace || owner.NamespacedName != service.Namespace+"/"+service.Name {
					return nil, fmt.Errorf("export %s/%s lacks trusted node-location metadata", export.Namespace, export.Name)
				}
				for _, port := range export.Spec.Ports {
					name := ""
					if port.Name != nil {
						name = *port.Name
					}
					if name != selected.Name || port.Port == nil {
						continue
					}
					if port.Protocol != nil && *port.Protocol != corev1.ProtocolTCP {
						return nil, fmt.Errorf("unsupported endpoint protocol")
					}
					if app.BackendPort != 0 && app.BackendPort != *port.Port {
						return nil, fmt.Errorf("conflicting target ports across clusters")
					}
					app.BackendPort = *port.Port
				}
				if export.Spec.ServiceGateway == nil {
					continue
				}
				if export.Spec.ServiceGateway.ServiceUID != acceptedUID {
					return nil, fmt.Errorf("export %s/%s node metadata belongs to a different Service UID", export.Namespace, export.Name)
				}
				for _, endpoint := range export.Spec.ServiceGateway.Endpoints {
					for _, address := range endpoint.Addresses {
						app.Backends = append(app.Backends, azure.Backend{PodIP: address, NodeIP: endpoint.NodeIP, Cluster: cluster.Cluster})
					}
				}
			}
		}
		if app.BackendPort == 0 {
			if len(app.Backends) > 0 {
				return nil, fmt.Errorf("no resolved endpoint target port")
			}
			// With no ready backends, keep the enrollment but withdraw membership.
			// The actual pod target port must be resolved before serving traffic.
			app.BackendPort = selected.Port
		}
		apps = append(apps, app)
		if err := r.routeStatus(ctx, route, g, true, "ResolvedRefs", "Accepted exported TCP backend"); err != nil {
			return nil, err
		}
	}
	if len(apps) > 16 {
		return nil, fmt.Errorf("at most 16 application VIPs per Gateway are supported")
	}
	return apps, nil
}

func (r *Reconciler) routeStatus(ctx context.Context, route *gatewayv1alpha2.TCPRoute, g *gatewayv1.Gateway, accepted bool, reason, message string) error {
	before := route.DeepCopy()
	var parents []gatewayv1.RouteParentStatus
	for _, parent := range route.Status.Parents {
		if parent.ControllerName != azure.ControllerName || string(parent.ParentRef.Name) != g.Name ||
			parent.ParentRef.Namespace != nil && string(*parent.ParentRef.Namespace) != g.Namespace {
			parents = append(parents, parent)
		}
	}
	status := metav1.ConditionFalse
	if accepted {
		status = metav1.ConditionTrue
	}
	for _, parent := range route.Spec.ParentRefs {
		if string(parent.Name) != g.Name || parent.Namespace != nil && string(*parent.Namespace) != g.Namespace {
			continue
		}
		parents = append(parents, gatewayv1.RouteParentStatus{ParentRef: parent, ControllerName: azure.ControllerName, Conditions: []metav1.Condition{
			{Type: "Accepted", Status: status, Reason: map[bool]string{true: "Accepted", false: "NoMatchingParent"}[accepted], Message: message, ObservedGeneration: route.Generation, LastTransitionTime: metav1.Now()},
			{Type: "ResolvedRefs", Status: status, Reason: reason, Message: message, ObservedGeneration: route.Generation, LastTransitionTime: metav1.Now()},
		}})
	}
	// Preserve transition timestamps when conditions have not changed.
	for i := range parents {
		for _, old := range before.Status.Parents {
			if old.ControllerName != parents[i].ControllerName || !reflect.DeepEqual(old.ParentRef, parents[i].ParentRef) {
				continue
			}
			for j := range parents[i].Conditions {
				next := &parents[i].Conditions[j]
				prev := meta.FindStatusCondition(old.Conditions, next.Type)
				if prev != nil && prev.Status == next.Status {
					next.LastTransitionTime = prev.LastTransitionTime
				}
			}
		}
	}
	route.Status.Parents = parents
	if reflect.DeepEqual(before.Status, route.Status) {
		return nil
	}
	return r.Client.Status().Patch(ctx, route, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	all := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []ctrl.Request {
		var gateways gatewayv1.GatewayList
		if err := r.Client.List(ctx, &gateways); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Cannot map Service Gateway dependencies")
			return nil
		}
		var requests []ctrl.Request
		for i := range gateways.Items {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&gateways.Items[i])})
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).Named("fleet-service-gateway").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		For(&gatewayv1.Gateway{}).
		Watches(&gatewayv1.GatewayClass{}, all).
		Watches(&gatewayv1alpha2.TCPRoute{}, all).
		Watches(&fleetnet.ServiceImport{}, all).
		Watches(&fleetnet.EndpointSliceExport{}, all).
		Watches(&fleetnet.InternalServiceExport{}, all).Complete(r)
}
