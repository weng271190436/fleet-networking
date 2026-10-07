/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

// Package multiclusterbackend reconciles MultiClusterBackend resources into member assignments.
package multiclusterbackend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clusterv1beta1 "go.goms.io/fleet/apis/cluster/v1beta1"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/common/hubconfig"
)

const (
	// ControllerName is the name of the MultiClusterBackend controller.
	ControllerName = "multiclusterbackend-controller"

	assignmentHashLength = 10

	// OriginCleanupFinalizer prevents assignment deletion until its AFD origin is withdrawn.
	OriginCleanupFinalizer = "networking.fleet.azure.com/afd-origin-cleanup"
)

// Reconciler reconciles MultiClusterBackend resources.
type Reconciler struct {
	client.Client

	// NewRequestToken creates correlation tokens for new assignments.
	// Production callers may leave it nil to use a cryptographically random token.
	NewRequestToken func() (string, error)
}

// Reconcile selects eligible members and reconciles one ServiceOriginAssignment per member.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	startTime := time.Now()
	backendRef := klog.KRef(req.Namespace, req.Name)
	klog.V(2).InfoS("Reconciliation starts", "multiClusterBackend", backendRef)
	defer func() {
		klog.V(2).InfoS("Reconciliation ends", "multiClusterBackend", backendRef, "latency", time.Since(startTime).Milliseconds())
	}()

	var backend fleetnetv1alpha1.MultiClusterBackend
	if err := r.Get(ctx, req.NamespacedName, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get MultiClusterBackend %s: %w", req.NamespacedName, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(&backend.Spec.ClusterSelector)
	if err != nil {
		return ctrl.Result{}, r.updateInvalidStatus(ctx, &backend, fleetnetv1alpha1.MultiClusterBackendReasonInvalid, err.Error())
	}

	selectedMembers, err := r.selectMembers(ctx, selector)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(selectedMembers) == 0 {
		if _, err := r.reconcileAssignments(ctx, &backend, nil); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.updateInvalidStatus(ctx, &backend, fleetnetv1alpha1.MultiClusterBackendReasonNoMatchingClusters, "no joined MemberCluster matches the selector"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if len(selectedMembers) > fleetnetv1alpha1.MultiClusterBackendSelectedMemberLimit {
		message := fmt.Sprintf("selector matched %d members, exceeding the limit of %d", len(selectedMembers), fleetnetv1alpha1.MultiClusterBackendSelectedMemberLimit)
		if err := r.updateInvalidStatus(ctx, &backend, fleetnetv1alpha1.MultiClusterBackendReasonTooManySelectedClusters, message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	assignments, err := r.reconcileAssignments(ctx, &backend, selectedMembers)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateSelectedStatus(ctx, &backend, selectedMembers, assignments); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) selectMembers(ctx context.Context, selector labels.Selector) ([]string, error) {
	var memberList clusterv1beta1.MemberClusterList
	if err := r.List(ctx, &memberList); err != nil {
		return nil, fmt.Errorf("list MemberClusters: %w", err)
	}

	selected := make([]string, 0, len(memberList.Items))
	for i := range memberList.Items {
		member := &memberList.Items[i]
		joined := meta.FindStatusCondition(member.Status.Conditions, string(clusterv1beta1.ConditionTypeMemberClusterJoined))
		if joined == nil || joined.Status != metav1.ConditionTrue {
			continue
		}
		if selector.Matches(labels.Set(member.Labels)) {
			selected = append(selected, member.Name)
		}
	}
	sort.Strings(selected)
	return selected, nil
}

func (r *Reconciler) reconcileAssignments(
	ctx context.Context,
	backend *fleetnetv1alpha1.MultiClusterBackend,
	selectedMembers []string,
) (map[string]*fleetnetv1alpha1.ServiceOriginAssignment, error) {
	desired := make(map[types.NamespacedName]string, len(selectedMembers))
	assignments := make(map[string]*fleetnetv1alpha1.ServiceOriginAssignment, len(selectedMembers))
	for _, memberName := range selectedMembers {
		key := types.NamespacedName{
			Namespace: fmt.Sprintf(hubconfig.HubNamespaceNameFormat, memberName),
			Name:      assignmentName(backend.Name, backend.UID, memberName),
		}
		desired[key] = memberName

		assignment, err := r.reconcileAssignment(ctx, backend, key)
		if err != nil {
			return nil, err
		}
		assignments[memberName] = assignment
	}

	var assignmentList fleetnetv1alpha1.ServiceOriginAssignmentList
	if err := r.List(ctx, &assignmentList); err != nil {
		return nil, fmt.Errorf("list ServiceOriginAssignments: %w", err)
	}
	for i := range assignmentList.Items {
		assignment := &assignmentList.Items[i]
		if assignment.Spec.BackendRef.Namespace != backend.Namespace ||
			assignment.Spec.BackendRef.Name != backend.Name {
			continue
		}
		if !strings.HasPrefix(assignment.Namespace, strings.TrimSuffix(hubconfig.HubNamespaceNameFormat, "%s")) {
			continue
		}
		key := types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name}
		if assignment.Spec.BackendRef.UID == backend.UID {
			if _, ok := desired[key]; ok {
				continue
			}
		}
		if assignmentInfrastructureReady(assignment) {
			if assignment.DeletionTimestamp.IsZero() {
				if err := r.Delete(ctx, assignment); err != nil && !apierrors.IsNotFound(err) {
					return nil, fmt.Errorf("mark ServiceOriginAssignment %s for deletion: %w", key, err)
				}
			}
			continue
		}
		if removeFinalizer(assignment, OriginCleanupFinalizer) {
			if err := r.Update(ctx, assignment); err != nil && !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("remove cleanup finalizer from ServiceOriginAssignment %s: %w", key, err)
			}
		}
		if err := r.Delete(ctx, assignment); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("delete deselected ServiceOriginAssignment %s: %w", key, err)
		}
	}
	return assignments, nil
}

func (r *Reconciler) reconcileAssignment(
	ctx context.Context,
	backend *fleetnetv1alpha1.MultiClusterBackend,
	key types.NamespacedName,
) (*fleetnetv1alpha1.ServiceOriginAssignment, error) {
	var assignment fleetnetv1alpha1.ServiceOriginAssignment
	if err := r.Get(ctx, key, &assignment); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get ServiceOriginAssignment %s: %w", key, err)
		}
		token, tokenErr := r.newRequestToken()
		if tokenErr != nil {
			return nil, fmt.Errorf("create request token for %s: %w", key, tokenErr)
		}
		assignment = fleetnetv1alpha1.ServiceOriginAssignment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  key.Namespace,
				Name:       key.Name,
				Finalizers: []string{OriginCleanupFinalizer},
			},
			Spec: desiredAssignmentSpec(backend, token),
		}
		if err := r.Create(ctx, &assignment); err != nil {
			return nil, fmt.Errorf("create ServiceOriginAssignment %s: %w", key, err)
		}
		return &assignment, nil
	}

	if assignment.Spec.BackendRef.UID != backend.UID {
		return nil, fmt.Errorf("ServiceOriginAssignment %s is owned by backend UID %q", key, assignment.Spec.BackendRef.UID)
	}
	desiredSpec := desiredAssignmentSpec(backend, assignment.Spec.Approval.RequestToken)
	specChanged := !equality.Semantic.DeepEqual(assignment.Spec, desiredSpec)
	finalizerChanged := addFinalizer(&assignment, OriginCleanupFinalizer)
	if !specChanged && !finalizerChanged {
		return &assignment, nil
	}
	if specChanged {
		assignment.Spec = desiredSpec
	}
	if err := r.Update(ctx, &assignment); err != nil {
		return nil, fmt.Errorf("update ServiceOriginAssignment %s: %w", key, err)
	}
	return &assignment, nil
}

func assignmentInfrastructureReady(assignment *fleetnetv1alpha1.ServiceOriginAssignment) bool {
	condition := meta.FindStatusCondition(
		assignment.Status.Conditions,
		string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
	)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func addFinalizer(obj metav1.Object, finalizer string) bool {
	for _, existing := range obj.GetFinalizers() {
		if existing == finalizer {
			return false
		}
	}
	obj.SetFinalizers(append(obj.GetFinalizers(), finalizer))
	return true
}

func removeFinalizer(obj metav1.Object, finalizer string) bool {
	finalizers := obj.GetFinalizers()
	for i, existing := range finalizers {
		if existing == finalizer {
			obj.SetFinalizers(append(finalizers[:i], finalizers[i+1:]...))
			return true
		}
	}
	return false
}

func desiredAssignmentSpec(backend *fleetnetv1alpha1.MultiClusterBackend, token string) fleetnetv1alpha1.ServiceOriginAssignmentSpec {
	return fleetnetv1alpha1.ServiceOriginAssignmentSpec{
		BackendRef: fleetnetv1alpha1.ServiceOriginAssignmentBackendReference{
			Namespace: backend.Namespace,
			Name:      backend.Name,
			UID:       backend.UID,
		},
		ServiceRef: fleetnetv1alpha1.ServiceOriginAssignmentServiceReference{
			Namespace: backend.Namespace,
			Name:      backend.Spec.Service.Name,
			Port:      backend.Spec.Service.Port,
		},
		Connectivity: fleetnetv1alpha1.ServiceOriginAssignmentConnectivity{
			Type: fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink,
		},
		Approval: fleetnetv1alpha1.ServiceOriginAssignmentApproval{
			RequestToken: token,
		},
	}
}

func assignmentName(backendName string, backendUID types.UID, memberName string) string {
	sum := sha256.Sum256([]byte(string(backendUID) + "\x00" + memberName))
	suffix := hex.EncodeToString(sum[:])[:assignmentHashLength]
	prefix := strings.ReplaceAll(backendName, ".", "-")
	maxPrefixLength := 63 - len(suffix) - 1
	if len(prefix) > maxPrefixLength {
		prefix = prefix[:maxPrefixLength]
	}
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = "backend"
	}
	return prefix + "-" + suffix
}

func (r *Reconciler) newRequestToken() (string, error) {
	if r.NewRequestToken != nil {
		return r.NewRequestToken()
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func (r *Reconciler) updateInvalidStatus(
	ctx context.Context,
	backend *fleetnetv1alpha1.MultiClusterBackend,
	reason fleetnetv1alpha1.MultiClusterBackendConditionReason,
	message string,
) error {
	old := backend.DeepCopy()
	backend.Status.ObservedGeneration = backend.Generation
	backend.Status.SelectedClusters = 0
	backend.Status.ReadyOrigins = 0
	backend.Status.Members = nil
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionAccepted),
		Status:             metav1.ConditionFalse,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: backend.Generation,
	})
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionResolvedRefs),
		Status:             metav1.ConditionFalse,
		Reason:             string(fleetnetv1alpha1.MultiClusterBackendReasonNoReadyOrigins),
		Message:            "no ready origins are available",
		ObservedGeneration: backend.Generation,
	})
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionProgrammed),
		Status:             metav1.ConditionUnknown,
		Reason:             string(fleetnetv1alpha1.MultiClusterBackendReasonProgramming),
		Message:            "backend programming is pending valid member selection",
		ObservedGeneration: backend.Generation,
	})
	return r.patchStatus(ctx, backend, old)
}

func (r *Reconciler) updateSelectedStatus(
	ctx context.Context,
	backend *fleetnetv1alpha1.MultiClusterBackend,
	selectedMembers []string,
	assignments map[string]*fleetnetv1alpha1.ServiceOriginAssignment,
) error {
	old := backend.DeepCopy()
	backend.Status.ObservedGeneration = backend.Generation
	backend.Status.SelectedClusters = int32(len(selectedMembers))
	backend.Status.ReadyOrigins = 0
	backend.Status.Members = make([]fleetnetv1alpha1.MultiClusterBackendMemberStatus, 0, len(selectedMembers))
	for _, memberName := range selectedMembers {
		ready := false
		reason := fleetnetv1alpha1.MultiClusterBackendMemberReasonPrivateLinkServiceNotReady
		if assignment := assignments[memberName]; assignment != nil {
			infrastructureReady := meta.FindStatusCondition(
				assignment.Status.Conditions,
				string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
			)
			if infrastructureReady != nil {
				ready = infrastructureReady.Status == metav1.ConditionTrue
				if !ready && infrastructureReady.Reason != "" {
					reason = fleetnetv1alpha1.MultiClusterBackendMemberConditionReason(infrastructureReady.Reason)
				}
			}
		}
		status := metav1.ConditionFalse
		if ready {
			status = metav1.ConditionTrue
			reason = fleetnetv1alpha1.MultiClusterBackendMemberReasonReady
			backend.Status.ReadyOrigins++
		}
		backend.Status.Members = append(backend.Status.Members, fleetnetv1alpha1.MultiClusterBackendMemberStatus{
			ClusterName: memberName,
			Conditions: []metav1.Condition{{
				Type:               string(fleetnetv1alpha1.MultiClusterBackendMemberConditionReady),
				Status:             status,
				Reason:             string(reason),
				ObservedGeneration: backend.Generation,
			}},
		})
	}
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(fleetnetv1alpha1.MultiClusterBackendReasonAccepted),
		Message:            "backend configuration and selected member count are valid",
		ObservedGeneration: backend.Generation,
	})
	resolvedStatus := metav1.ConditionFalse
	resolvedReason := fleetnetv1alpha1.MultiClusterBackendReasonNoReadyOrigins
	resolvedMessage := "no selected member has ready origin infrastructure"
	if backend.Status.ReadyOrigins > 0 {
		resolvedStatus = metav1.ConditionTrue
		resolvedReason = fleetnetv1alpha1.MultiClusterBackendReasonReadyOriginsAvailable
		resolvedMessage = "at least one selected member has ready origin infrastructure"
	}
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionResolvedRefs),
		Status:             resolvedStatus,
		Reason:             string(resolvedReason),
		Message:            resolvedMessage,
		ObservedGeneration: backend.Generation,
	})
	meta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               string(fleetnetv1alpha1.MultiClusterBackendConditionProgrammed),
		Status:             metav1.ConditionUnknown,
		Reason:             string(fleetnetv1alpha1.MultiClusterBackendReasonProgramming),
		Message:            "Azure programming state is not yet available",
		ObservedGeneration: backend.Generation,
	})
	return r.patchStatus(ctx, backend, old)
}

func (r *Reconciler) patchStatus(
	ctx context.Context,
	backend *fleetnetv1alpha1.MultiClusterBackend,
	old *fleetnetv1alpha1.MultiClusterBackend,
) error {
	if equality.Semantic.DeepEqual(old.Status, backend.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, backend, client.MergeFrom(old)); err != nil {
		return fmt.Errorf("patch MultiClusterBackend status %s/%s: %w", backend.Namespace, backend.Name, err)
	}
	return nil
}

// SetupWithManager registers the MultiClusterBackend controller and its watches.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(ControllerName).
		For(&fleetnetv1alpha1.MultiClusterBackend{}).
		Watches(&clusterv1beta1.MemberCluster{}, handler.EnqueueRequestsFromMapFunc(r.requestsForMemberCluster)).
		Watches(&fleetnetv1alpha1.ServiceOriginAssignment{}, handler.EnqueueRequestsFromMapFunc(r.requestsForAssignment)).
		Complete(r)
}

func (r *Reconciler) requestsForMemberCluster(ctx context.Context, _ client.Object) []reconcile.Request {
	var backends fleetnetv1alpha1.MultiClusterBackendList
	if err := r.List(ctx, &backends); err != nil {
		klog.ErrorS(err, "Failed to list MultiClusterBackends after MemberCluster event")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(backends.Items))
	for i := range backends.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: backends.Items[i].Namespace,
			Name:      backends.Items[i].Name,
		}})
	}
	return requests
}

func (r *Reconciler) requestsForAssignment(_ context.Context, obj client.Object) []reconcile.Request {
	assignment, ok := obj.(*fleetnetv1alpha1.ServiceOriginAssignment)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: assignment.Spec.BackendRef.Namespace,
		Name:      assignment.Spec.BackendRef.Name,
	}}}
}
