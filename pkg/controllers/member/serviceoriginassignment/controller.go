/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

// Package serviceoriginassignment discovers member-local Azure origin infrastructure.
package serviceoriginassignment

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/common/objectmeta"
)

const (
	// ControllerName is the name of the ServiceOriginAssignment controller.
	ControllerName = "serviceoriginassignment-controller"

	defaultInfrastructurePollInterval = 30 * time.Second
)

// LoadBalancerClient is the read-only ARM surface used for load balancer discovery.
type LoadBalancerClient interface {
	List(ctx context.Context, resourceGroupName string) ([]*armnetwork.LoadBalancer, error)
}

// PrivateLinkServiceClient is the read-only ARM surface used for Private Link Service discovery.
type PrivateLinkServiceClient interface {
	List(ctx context.Context, resourceGroupName string) ([]*armnetwork.PrivateLinkService, error)
}

// Reconciler discovers origin infrastructure for a ServiceOriginAssignment.
type Reconciler struct {
	HubClient    client.Client
	MemberClient client.Client

	ResourceGroupName        string
	LoadBalancerClient       LoadBalancerClient
	PrivateLinkServiceClient PrivateLinkServiceClient

	InfrastructurePollInterval time.Duration
}

//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.fleet.azure.com,resources=serviceoriginassignments,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.fleet.azure.com,resources=serviceoriginassignments/status,verbs=get;update;patch

// Reconcile resolves the referenced Service and publishes discovered Azure origin facts.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	assignmentRef := klog.KRef(req.Namespace, req.Name)
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "serviceOriginAssignment", assignmentRef)
	defer func() {
		klog.V(2).InfoS("Reconciliation ends", "serviceOriginAssignment", assignmentRef, "latency", time.Since(startTime).Milliseconds())
	}()

	var assignment fleetnetv1alpha1.ServiceOriginAssignment
	if err := r.HubClient.Get(ctx, req.NamespacedName, &assignment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ServiceOriginAssignment %s: %w", req.NamespacedName, err)
	}

	status := desiredStatus(&assignment)
	serviceKey := types.NamespacedName{
		Namespace: assignment.Spec.ServiceRef.Namespace,
		Name:      assignment.Spec.ServiceRef.Name,
	}
	var service corev1.Service
	if err := r.MemberClient.Get(ctx, serviceKey, &service); err != nil {
		if apierrors.IsNotFound(err) {
			setDiscoveryConditions(&status, assignment.Generation,
				metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceNotFound,
				fmt.Sprintf("Service %s was not found in the member cluster", serviceKey),
				metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
				"load balancer discovery cannot start until the Service exists")
			return ctrl.Result{}, r.updateStatus(ctx, &assignment, status)
		}
		return ctrl.Result{}, fmt.Errorf("get member Service %s: %w", serviceKey, err)
	}

	if !serviceHasPort(&service, assignment.Spec.ServiceRef.Port) {
		setDiscoveryConditions(&status, assignment.Generation,
			metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonPortNotFound,
			fmt.Sprintf("Service %s does not expose numeric port %d", serviceKey, assignment.Spec.ServiceRef.Port),
			metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			"load balancer discovery cannot start until the requested Service port exists")
		return ctrl.Result{}, r.updateStatus(ctx, &assignment, status)
	}

	setCondition(&status, assignment.Generation,
		fleetnetv1alpha1.ServiceOriginAssignmentConditionServiceResolved,
		metav1.ConditionTrue, fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
		fmt.Sprintf("Service %s exposes numeric port %d", serviceKey, assignment.Spec.ServiceRef.Port))

	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		setCondition(&status, assignment.Generation,
			fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
			metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			fmt.Sprintf("Service %s must have type LoadBalancer", serviceKey))
		return ctrl.Result{}, r.updateStatus(ctx, &assignment, status)
	}
	if !strings.EqualFold(strings.TrimSpace(service.Annotations[objectmeta.ServiceAnnotationAzureLoadBalancerInternal]), "true") {
		setCondition(&status, assignment.Generation,
			fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
			metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			fmt.Sprintf("Service %s must set annotation %s to true", serviceKey, objectmeta.ServiceAnnotationAzureLoadBalancerInternal))
		return ctrl.Result{}, r.updateStatus(ctx, &assignment, status)
	}

	ingressIP := readyIngressIP(&service)
	if ingressIP == "" {
		setCondition(&status, assignment.Generation,
			fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
			metav1.ConditionFalse, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			fmt.Sprintf("Service %s is waiting for a load balancer ingress IP", serviceKey))
		if err := r.updateStatus(ctx, &assignment, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
	}

	resourceGroupName := strings.TrimSpace(service.Annotations[objectmeta.ServiceAnnotationLoadBalancerResourceGroup])
	if resourceGroupName == "" {
		resourceGroupName = r.ResourceGroupName
	}
	origin, infrastructureReason, infrastructureMessage, err := r.discoverOrigin(ctx, resourceGroupName, ingressIP)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("discover Azure origin for Service %s: %w", serviceKey, err)
	}
	if origin == nil {
		setCondition(&status, assignment.Generation,
			fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
			metav1.ConditionFalse, infrastructureReason, infrastructureMessage)
		if err := r.updateStatus(ctx, &assignment, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
	}

	status.Origin = origin
	setCondition(&status, assignment.Generation,
		fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
		metav1.ConditionTrue, fleetnetv1alpha1.ServiceOriginAssignmentReasonInfrastructureReady,
		fmt.Sprintf("internal load balancer %s is associated with Private Link Service %s", ingressIP, origin.PrivateLinkServiceID))
	return ctrl.Result{}, r.updateStatus(ctx, &assignment, status)
}

func (r *Reconciler) discoverOrigin(
	ctx context.Context,
	resourceGroupName, ingressIP string,
) (*fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus, fleetnetv1alpha1.ServiceOriginAssignmentConditionReason, string, error) {
	loadBalancers, err := r.LoadBalancerClient.List(ctx, resourceGroupName)
	if err != nil {
		return nil, "", "", fmt.Errorf("list load balancers in resource group %q: %w", resourceGroupName, err)
	}
	frontendID := matchingFrontendID(loadBalancers, ingressIP)
	if frontendID == "" {
		return nil, fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			fmt.Sprintf("Azure load balancer frontend with private IP %s was not found in resource group %s", ingressIP, resourceGroupName), nil
	}

	privateLinkServices, err := r.PrivateLinkServiceClient.List(ctx, resourceGroupName)
	if err != nil {
		return nil, "", "", fmt.Errorf("list Private Link Services in resource group %q: %w", resourceGroupName, err)
	}
	for _, privateLinkService := range privateLinkServices {
		if !privateLinkServiceReferencesFrontend(privateLinkService, frontendID) {
			continue
		}
		if privateLinkService.ID == nil || strings.TrimSpace(*privateLinkService.ID) == "" ||
			privateLinkService.Location == nil || strings.TrimSpace(*privateLinkService.Location) == "" {
			continue
		}
		return &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
			AzureLocation:        strings.ToLower(strings.TrimSpace(*privateLinkService.Location)),
			LoadBalancerAddress:  ingressIP,
			PrivateLinkServiceID: strings.TrimSpace(*privateLinkService.ID),
		}, "", "", nil
	}

	return nil, fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkServiceNotReady,
		fmt.Sprintf("Private Link Service associated with load balancer frontend %s was not found in resource group %s", frontendID, resourceGroupName), nil
}

func (r *Reconciler) updateStatus(
	ctx context.Context,
	assignment *fleetnetv1alpha1.ServiceOriginAssignment,
	status fleetnetv1alpha1.ServiceOriginAssignmentStatus,
) error {
	if equality.Semantic.DeepEqual(assignment.Status, status) {
		return nil
	}
	assignment.Status = status
	if err := r.HubClient.Status().Update(ctx, assignment); err != nil {
		return fmt.Errorf("update ServiceOriginAssignment %s/%s status: %w", assignment.Namespace, assignment.Name, err)
	}
	return nil
}

func (r *Reconciler) pollInterval() time.Duration {
	if r.InfrastructurePollInterval > 0 {
		return r.InfrastructurePollInterval
	}
	return defaultInfrastructurePollInterval
}

func desiredStatus(assignment *fleetnetv1alpha1.ServiceOriginAssignment) fleetnetv1alpha1.ServiceOriginAssignmentStatus {
	status := fleetnetv1alpha1.ServiceOriginAssignmentStatus{
		ObservedGeneration: assignment.Generation,
	}
	for _, condition := range assignment.Status.Conditions {
		if condition.Type == string(fleetnetv1alpha1.ServiceOriginAssignmentConditionServiceResolved) ||
			condition.Type == string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady) {
			status.Conditions = append(status.Conditions, condition)
		}
	}
	return status
}

func setDiscoveryConditions(
	status *fleetnetv1alpha1.ServiceOriginAssignmentStatus,
	generation int64,
	serviceStatus metav1.ConditionStatus,
	serviceReason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason,
	serviceMessage string,
	infrastructureStatus metav1.ConditionStatus,
	infrastructureReason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason,
	infrastructureMessage string,
) {
	setCondition(status, generation, fleetnetv1alpha1.ServiceOriginAssignmentConditionServiceResolved,
		serviceStatus, serviceReason, serviceMessage)
	setCondition(status, generation, fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
		infrastructureStatus, infrastructureReason, infrastructureMessage)
}

func setCondition(
	status *fleetnetv1alpha1.ServiceOriginAssignmentStatus,
	generation int64,
	conditionType fleetnetv1alpha1.ServiceOriginAssignmentConditionType,
	conditionStatus metav1.ConditionStatus,
	reason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason,
	message string,
) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             conditionStatus,
		ObservedGeneration: generation,
		Reason:             string(reason),
		Message:            message,
	})
}

func serviceHasPort(service *corev1.Service, port int32) bool {
	for _, servicePort := range service.Spec.Ports {
		if servicePort.Port == port {
			return true
		}
	}
	return false
}

func readyIngressIP(service *corev1.Service) string {
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		ip := strings.TrimSpace(ingress.IP)
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

func matchingFrontendID(loadBalancers []*armnetwork.LoadBalancer, ingressIP string) string {
	for _, loadBalancer := range loadBalancers {
		if loadBalancer == nil || loadBalancer.Properties == nil {
			continue
		}
		for _, frontend := range loadBalancer.Properties.FrontendIPConfigurations {
			if frontend == nil || frontend.ID == nil || frontend.Properties == nil ||
				frontend.Properties.PrivateIPAddress == nil {
				continue
			}
			if strings.TrimSpace(*frontend.Properties.PrivateIPAddress) == ingressIP {
				return strings.TrimSpace(*frontend.ID)
			}
		}
	}
	return ""
}

func privateLinkServiceReferencesFrontend(privateLinkService *armnetwork.PrivateLinkService, frontendID string) bool {
	if privateLinkService == nil || privateLinkService.Properties == nil {
		return false
	}
	for _, frontend := range privateLinkService.Properties.LoadBalancerFrontendIPConfigurations {
		if frontend != nil && frontend.ID != nil && strings.EqualFold(strings.TrimSpace(*frontend.ID), frontendID) {
			return true
		}
	}
	return false
}

// SetupWithManager registers the assignment watch on the scoped hub manager and the Service watch on the member manager.
func (r *Reconciler) SetupWithManager(hubManager, memberManager manager.Manager) error {
	serviceHandler := handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, service *corev1.Service) []reconcile.Request {
		var assignments fleetnetv1alpha1.ServiceOriginAssignmentList
		if err := r.HubClient.List(ctx, &assignments); err != nil {
			klog.ErrorS(err, "Failed to list ServiceOriginAssignments for a member Service", "service", klog.KObj(service))
			return nil
		}
		requests := make([]reconcile.Request, 0)
		for i := range assignments.Items {
			assignment := &assignments.Items[i]
			if assignment.Spec.ServiceRef.Namespace == service.Namespace && assignment.Spec.ServiceRef.Name == service.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
				})
			}
		}
		return requests
	})

	return ctrl.NewControllerManagedBy(hubManager).
		For(&fleetnetv1alpha1.ServiceOriginAssignment{}).
		WatchesRawSource(source.Kind(memberManager.GetCache(), &corev1.Service{}, serviceHandler)).
		Complete(r)
}
