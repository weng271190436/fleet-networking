/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/common/objectmeta"
)

const (
	testResourceGroup = "member-node-rg"
	testIngressIP     = "10.0.1.4"
	testFrontendID    = "/subscriptions/sub/resourceGroups/member-node-rg/providers/Microsoft.Network/loadBalancers/kubernetes/frontendIPConfigurations/frontend"
	testPLSID         = "/subscriptions/sub/resourceGroups/member-node-rg/providers/Microsoft.Network/privateLinkServices/echo"
)

func TestReconcileDiscoveryConditions(t *testing.T) {
	tests := []struct {
		name                     string
		service                  *corev1.Service
		loadBalancers            []*armnetwork.LoadBalancer
		privateLinkServices      []*armnetwork.PrivateLinkService
		wantServiceStatus        metav1.ConditionStatus
		wantServiceReason        fleetnetv1alpha1.ServiceOriginAssignmentConditionReason
		wantInfrastructureStatus metav1.ConditionStatus
		wantInfrastructureReason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason
		wantOrigin               *fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus
		wantRequeue              bool
	}{
		{
			name:                     "service not found",
			wantServiceStatus:        metav1.ConditionFalse,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceNotFound,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
		},
		{
			name:                     "port not found",
			service:                  testService(corev1.ServiceTypeLoadBalancer, true, true, 81),
			wantServiceStatus:        metav1.ConditionFalse,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonPortNotFound,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
		},
		{
			name:                     "service is not load balancer type",
			service:                  testService(corev1.ServiceTypeClusterIP, true, true, 80),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
		},
		{
			name:                     "service is not internal",
			service:                  testService(corev1.ServiceTypeLoadBalancer, false, true, 80),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
		},
		{
			name:                     "load balancer ingress is pending",
			service:                  testService(corev1.ServiceTypeLoadBalancer, true, false, 80),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			wantRequeue:              true,
		},
		{
			name:                     "azure load balancer frontend is pending",
			service:                  testService(corev1.ServiceTypeLoadBalancer, true, true, 80),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonLoadBalancerNotReady,
			wantRequeue:              true,
		},
		{
			name:                     "private link service is pending",
			service:                  testService(corev1.ServiceTypeLoadBalancer, true, true, 80),
			loadBalancers:            matchingLoadBalancers(),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionFalse,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkServiceNotReady,
			wantRequeue:              true,
		},
		{
			name:                     "origin infrastructure is ready",
			service:                  testService(corev1.ServiceTypeLoadBalancer, true, true, 80),
			loadBalancers:            matchingLoadBalancers(),
			privateLinkServices:      matchingPrivateLinkServices(),
			wantServiceStatus:        metav1.ConditionTrue,
			wantServiceReason:        fleetnetv1alpha1.ServiceOriginAssignmentReasonServiceResolved,
			wantInfrastructureStatus: metav1.ConditionTrue,
			wantInfrastructureReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonInfrastructureReady,
			wantOrigin: &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
				AzureLocation:        "eastus",
				LoadBalancerAddress:  testIngressIP,
				PrivateLinkServiceID: testPLSID,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assignment := testAssignment()
			memberObjects := make([]client.Object, 0, 1)
			if tc.service != nil {
				memberObjects = append(memberObjects, tc.service)
			}
			memberClient := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(memberObjects...).
				Build()
			hubClient := newCountingHubClient(t, assignment)
			reconciler := &Reconciler{
				HubClient:                  hubClient,
				MemberClient:               memberClient,
				ResourceGroupName:          testResourceGroup,
				LoadBalancerClient:         &fakeLoadBalancerClient{items: tc.loadBalancers},
				PrivateLinkServiceClient:   &fakePrivateLinkServiceClient{items: tc.privateLinkServices},
				InfrastructurePollInterval: time.Second,
			}

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
			})
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if got := result.RequeueAfter > 0; got != tc.wantRequeue {
				t.Errorf("RequeueAfter > 0 = %t, want %t", got, tc.wantRequeue)
			}

			got := getAssignment(t, hubClient.Client, assignment)
			assertCondition(t, got.Status.Conditions,
				fleetnetv1alpha1.ServiceOriginAssignmentConditionServiceResolved,
				tc.wantServiceStatus, tc.wantServiceReason)
			assertCondition(t, got.Status.Conditions,
				fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady,
				tc.wantInfrastructureStatus, tc.wantInfrastructureReason)
			if !meta.IsStatusConditionPresentAndEqual(got.Status.Conditions,
				string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
				tc.wantInfrastructureStatus) {
				t.Errorf("InfrastructureReady condition = %#v", got.Status.Conditions)
			}
			if diff := originDifference(got.Status.Origin, tc.wantOrigin); diff != "" {
				t.Errorf("origin mismatch: %s", diff)
			}
			if got.Status.ObservedGeneration != assignment.Generation {
				t.Errorf("ObservedGeneration = %d, want %d", got.Status.ObservedGeneration, assignment.Generation)
			}
			if hubClient.updateCalls != 0 {
				t.Errorf("regular hub Update calls = %d, want 0", hubClient.updateCalls)
			}
			if hubClient.statusUpdateCalls != 1 {
				t.Errorf("hub status Update calls = %d, want 1", hubClient.statusUpdateCalls)
			}
			if got.Spec != assignment.Spec {
				t.Errorf("assignment spec changed: got %#v, want %#v", got.Spec, assignment.Spec)
			}
		})
	}
}

func TestDesiredStatusPreservesOrigin(t *testing.T) {
	assignment := testAssignment()
	assignment.Status.ObservedGeneration = assignment.Generation
	assignment.Status.Origin = &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
		AzureLocation:        "eastus2",
		LoadBalancerAddress:  testIngressIP,
		PrivateLinkServiceID: testPLSID,
	}

	got := desiredStatus(assignment)
	if got.Origin == nil {
		t.Fatal("desiredStatus() Origin = nil, want discovered origin")
	}
	if got.Origin == assignment.Status.Origin {
		t.Fatal("desiredStatus() reused the source Origin pointer, want deep copy")
	}
	if diff := originDifference(got.Origin, assignment.Status.Origin); diff != "" {
		t.Errorf("desiredStatus() origin mismatch: %s", diff)
	}
}

func TestReconcileUnchangedStatusDoesNotWrite(t *testing.T) {
	assignment := testAssignment()
	memberClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testService(corev1.ServiceTypeLoadBalancer, true, true, 80)).
		Build()
	hubClient := newCountingHubClient(t, assignment)
	reconciler := &Reconciler{
		HubClient:                  hubClient,
		MemberClient:               memberClient,
		ResourceGroupName:          testResourceGroup,
		LoadBalancerClient:         &fakeLoadBalancerClient{items: matchingLoadBalancers()},
		PrivateLinkServiceClient:   &fakePrivateLinkServiceClient{items: matchingPrivateLinkServices()},
		InfrastructurePollInterval: time.Second,
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if hubClient.statusUpdateCalls != 1 {
		t.Errorf("hub status Update calls = %d, want 1", hubClient.statusUpdateCalls)
	}
	if hubClient.updateCalls != 0 {
		t.Errorf("regular hub Update calls = %d, want 0", hubClient.updateCalls)
	}
}

type fakeLoadBalancerClient struct {
	items []*armnetwork.LoadBalancer
	err   error
}

func (f *fakeLoadBalancerClient) List(_ context.Context, _ string) ([]*armnetwork.LoadBalancer, error) {
	return f.items, f.err
}

type fakePrivateLinkServiceClient struct {
	items []*armnetwork.PrivateLinkService
	err   error
}

func (f *fakePrivateLinkServiceClient) List(_ context.Context, _ string) ([]*armnetwork.PrivateLinkService, error) {
	return f.items, f.err
}

type countingHubClient struct {
	client.Client
	updateCalls       int
	statusUpdateCalls int
}

func newCountingHubClient(t *testing.T, assignment *fleetnetv1alpha1.ServiceOriginAssignment) *countingHubClient {
	t.Helper()
	return &countingHubClient{
		Client: fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithStatusSubresource(&fleetnetv1alpha1.ServiceOriginAssignment{}).
			WithObjects(assignment).
			Build(),
	}
}

func (c *countingHubClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updateCalls++
	return c.Client.Update(ctx, obj, opts...)
}

func (c *countingHubClient) Status() client.SubResourceWriter {
	return &countingStatusWriter{SubResourceWriter: c.Client.Status(), client: c}
}

type countingStatusWriter struct {
	client.SubResourceWriter
	client *countingHubClient
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.client.statusUpdateCalls++
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error = %v", err)
	}
	if err := fleetnetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(fleet networking) error = %v", err)
	}
	return scheme
}

func testAssignment() *fleetnetv1alpha1.ServiceOriginAssignment {
	return &fleetnetv1alpha1.ServiceOriginAssignment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "fleet-member-member-a",
			Name:       "echo-assignment",
			UID:        testAssignmentUID,
			Generation: 3,
		},
		Spec: fleetnetv1alpha1.ServiceOriginAssignmentSpec{
			ServiceRef: fleetnetv1alpha1.ServiceOriginAssignmentServiceReference{
				Namespace: "app",
				Name:      "echo",
				Port:      80,
			},
			Approval: fleetnetv1alpha1.ServiceOriginAssignmentApproval{
				RequestToken: testRequestToken,
			},
		},
	}
}

func testService(serviceType corev1.ServiceType, internal, ingressReady bool, port int32) *corev1.Service {
	annotations := map[string]string{}
	if internal {
		annotations[objectmeta.ServiceAnnotationAzureLoadBalancerInternal] = "true"
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "app",
			Name:        "echo",
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: serviceType,
			Ports: []corev1.ServicePort{{
				Port: port,
			}},
		},
	}
	if ingressReady {
		service.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: testIngressIP}}
	}
	return service
}

func matchingLoadBalancers() []*armnetwork.LoadBalancer {
	return []*armnetwork.LoadBalancer{{
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
				ID: ptr.To(testFrontendID),
				Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
					PrivateIPAddress: ptr.To(testIngressIP),
				},
			}},
		},
	}}
}

func matchingPrivateLinkServices() []*armnetwork.PrivateLinkService {
	return []*armnetwork.PrivateLinkService{{
		ID:       ptr.To(testPLSID),
		Location: ptr.To("eastus"),
		Properties: &armnetwork.PrivateLinkServiceProperties{
			LoadBalancerFrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{{
				ID: ptr.To(testFrontendID),
			}},
		},
	}}
}

func getAssignment(t *testing.T, c client.Client, assignment *fleetnetv1alpha1.ServiceOriginAssignment) *fleetnetv1alpha1.ServiceOriginAssignment {
	t.Helper()
	var got fleetnetv1alpha1.ServiceOriginAssignment
	key := types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name}
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("Get(ServiceOriginAssignment) error = %v", err)
	}
	return &got
}

func assertCondition(
	t *testing.T,
	conditions []metav1.Condition,
	conditionType fleetnetv1alpha1.ServiceOriginAssignmentConditionType,
	wantStatus metav1.ConditionStatus,
	wantReason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason,
) {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, string(conditionType))
	if condition == nil {
		t.Fatalf("%s condition not found in %#v", conditionType, conditions)
	}
	if condition.Status != wantStatus || condition.Reason != string(wantReason) {
		t.Errorf("%s condition = (%s, %s), want (%s, %s)",
			conditionType, condition.Status, condition.Reason, wantStatus, wantReason)
	}
	if condition.Message == "" {
		t.Errorf("%s condition has an empty actionable message", conditionType)
	}
}

func originDifference(got, want *fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus) string {
	switch {
	case got == nil && want == nil:
		return ""
	case got == nil:
		return "got nil"
	case want == nil:
		return "want nil"
	case *got != *want:
		return "values differ"
	default:
		return ""
	}
}
