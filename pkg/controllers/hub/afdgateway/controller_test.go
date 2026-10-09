/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package afdgateway

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/annotations"
	"go.goms.io/fleet-networking/pkg/controllers/hub/gatewaymodel"
	"go.goms.io/fleet-networking/pkg/controllers/hub/multiclusterbackend"
	"go.goms.io/fleet-networking/pkg/providers/azure/frontdoor"
)

const testWAFPolicyID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/security-rg/providers/Microsoft.Network/frontdoorWebApplicationFirewallPolicies/poc-waf"

type fakeProvider struct {
	result       frontdoor.Result
	reconcileErr error
	deleteErr    error
	withdrawErr  error
	reconciles   int
	deletes      int
	withdraws    int
}

func TestSetListenersSetsRequiredTransitionTimes(t *testing.T) {
	gateway := validGateway()
	setListeners(gateway, metav1.ConditionTrue, string(gatewayv1.ListenerReasonAccepted), "ready", 1)

	if len(gateway.Status.Listeners) != 1 {
		t.Fatalf("listener status count = %d, want 1", len(gateway.Status.Listeners))
	}
	for _, condition := range gateway.Status.Listeners[0].Conditions {
		if condition.LastTransitionTime.IsZero() {
			t.Errorf("condition %q transition time is zero", condition.Type)
		}
	}
}

func (p *fakeProvider) Reconcile(context.Context, gatewaymodel.GlobalGateway) (frontdoor.Result, error) {
	p.reconciles++
	return p.result, p.reconcileErr
}

func (p *fakeProvider) Delete(context.Context, gatewaymodel.GlobalGateway) error {
	p.deletes++
	return p.deleteErr
}

func (p *fakeProvider) WithdrawOrigins(context.Context, gatewaymodel.GlobalGateway, string, []string) error {
	p.withdraws++
	return p.withdrawErr
}

func TestValidateGateway(t *testing.T) {
	base := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			annotations.AFDSKUAnnotation:         string(annotations.SKUPremium),
			annotations.AFDWAFPolicyIDAnnotation: testWAFPolicyID,
		}},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: GatewayClassName,
			Listeners: []gatewayv1.Listener{{
				Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80,
			}},
		},
	}
	tests := []struct {
		name    string
		mutate  func(*gatewayv1.Gateway)
		wantErr bool
	}{
		{name: "supported"},
		{name: "wrong class", mutate: func(g *gatewayv1.Gateway) { g.Spec.GatewayClassName = "other" }, wantErr: true},
		{name: "HTTPS unsupported", mutate: func(g *gatewayv1.Gateway) { g.Spec.Listeners[0].Protocol = gatewayv1.HTTPSProtocolType }, wantErr: true},
		{name: "non premium", mutate: func(g *gatewayv1.Gateway) {
			g.Annotations[annotations.AFDSKUAnnotation] = string(annotations.SKUStandard)
		}, wantErr: true},
		{name: "WAF required", mutate: func(g *gatewayv1.Gateway) { delete(g.Annotations, annotations.AFDWAFPolicyIDAnnotation) }, wantErr: true},
		{name: "invalid WAF", mutate: func(g *gatewayv1.Gateway) { g.Annotations[annotations.AFDWAFPolicyIDAnnotation] = "bad" }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := base.DeepCopy()
			if tt.mutate != nil {
				tt.mutate(gateway)
			}
			_, err := validateGateway(gateway)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateGateway() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestBuildDesiredRouteGatesBackends(t *testing.T) {
	scheme := testScheme(t)
	backend := readyBackend()
	assignment := readyAssignment(backend, true)
	route := attachedRoute()
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(backend, assignment).Build()

	model, approved, err := buildRoute(context.Background(), client, route, "global")
	if err != nil {
		t.Fatalf("buildRoute() error = %v", err)
	}
	if !approved || len(model.Backends) != 1 || len(model.Backends[0].Origins) != 1 {
		t.Fatalf("buildRoute() = %#v, approved = %t, want one approved origin", model, approved)
	}

	port := gatewayv1.PortNumber(80)
	route.Spec.Rules[0].BackendRefs[0].Port = &port
	if _, _, err := buildRoute(context.Background(), client, route, "global"); err == nil {
		t.Fatal("buildRoute() accepted backendRef.port")
	}
	route.Spec.Rules[0].BackendRefs[0].Port = nil
	other := gatewayv1.Namespace("other")
	route.Spec.Rules[0].BackendRefs[0].Namespace = &other
	if _, _, err := buildRoute(context.Background(), client, route, "global"); err == nil {
		t.Fatal("buildRoute() accepted cross-namespace backend")
	}
}

func TestReconcilePublishesStatusOnlyAfterProviderAndApproval(t *testing.T) {
	scheme := testScheme(t)
	gatewayClass := acceptedGatewayClass()
	gateway := validGateway()
	route := attachedRoute()
	backend := readyBackend()
	assignment := readyAssignment(backend, true)
	provider := &fakeProvider{result: frontdoor.Result{Ready: true, EndpointHostName: "global.azurefd.net"}}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GatewayClass{}, &fleetnetv1alpha1.MultiClusterBackend{}).
		WithObjects(gatewayClass, gateway, route, backend, assignment).Build()
	reconciler := &Reconciler{Client: client, Provider: provider}

	if _, err := reconciler.Reconcile(context.Background(), requestFor(gateway)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var got gatewayv1.Gateway
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) {
		t.Fatalf("Gateway conditions = %#v, want Programmed=True", got.Status.Conditions)
	}
	if got.Status.Conditions[0].ObservedGeneration != got.Generation {
		t.Fatalf("Gateway observed generation not current: %#v", got.Status.Conditions)
	}
	if len(got.Status.Addresses) != 1 || got.Status.Addresses[0].Value != "global.azurefd.net" {
		t.Fatalf("Gateway addresses = %#v, want default AFD hostname", got.Status.Addresses)
	}
	if !containsString(got.Finalizers, GatewayFinalizer) || provider.reconciles != 1 {
		t.Fatalf("finalizers = %v, reconciles = %d", got.Finalizers, provider.reconciles)
	}

	var gotRoute gatewayv1.HTTPRoute
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: route.Namespace, Name: route.Name}, &gotRoute); err != nil {
		t.Fatal(err)
	}
	parent := gotRoute.Status.Parents[0]
	if !meta.IsStatusConditionTrue(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) ||
		!meta.IsStatusConditionTrue(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) ||
		!meta.IsStatusConditionTrue(parent.Conditions, routeConditionProgrammed) {
		t.Fatalf("HTTPRoute conditions = %#v, want all true", parent.Conditions)
	}
}

func TestReconcileProviderErrorPreservesProgrammedStatus(t *testing.T) {
	scheme := testScheme(t)
	gateway := validGateway()
	gateway.Status.Conditions = []metav1.Condition{{
		Type: string(gatewayv1.GatewayConditionProgrammed), Status: metav1.ConditionTrue,
		Reason: string(gatewayv1.GatewayReasonProgrammed), ObservedGeneration: gateway.Generation,
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GatewayClass{}, &fleetnetv1alpha1.MultiClusterBackend{}).
		WithObjects(acceptedGatewayClass(), gateway, attachedRoute(), readyBackend(), readyAssignment(readyBackend(), true)).Build()
	reconciler := &Reconciler{Client: client, Provider: &fakeProvider{reconcileErr: errors.New("transient")}}

	if _, err := reconciler.Reconcile(context.Background(), requestFor(gateway)); err == nil {
		t.Fatal("Reconcile() error = nil, want provider error")
	}
	var got gatewayv1.Gateway
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) {
		t.Fatalf("Gateway conditions = %#v, want existing Programmed=True preserved", got.Status.Conditions)
	}
}

func TestReconcileDoesNotProgramBeforeApprovalAndProviderReady(t *testing.T) {
	tests := []struct {
		name       string
		approved   bool
		providerOK bool
	}{
		{name: "approval pending", providerOK: true},
		{name: "provider pending", approved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testScheme(t)
			gateway := validGateway()
			backend := readyBackend()
			client := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GatewayClass{}, &fleetnetv1alpha1.MultiClusterBackend{}).
				WithObjects(acceptedGatewayClass(), gateway, attachedRoute(), backend, readyAssignment(backend, tt.approved)).Build()
			reconciler := &Reconciler{Client: client, Provider: &fakeProvider{result: frontdoor.Result{Ready: tt.providerOK}}}
			if _, err := reconciler.Reconcile(context.Background(), requestFor(gateway)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			var got gatewayv1.Gateway
			if err := client.Get(context.Background(), clientObjectKey(gateway), &got); err != nil {
				t.Fatal(err)
			}
			if meta.IsStatusConditionTrue(got.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) {
				t.Fatalf("Gateway conditions = %#v, Programmed became true early", got.Status.Conditions)
			}
		})
	}
}

func TestReconcileDeleteLifecycle(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name          string
		withFinalizer bool
		deleteErr     error
		wantDeletes   int
		wantFinalizer bool
		wantErr       bool
	}{
		{name: "never programmed does not block"},
		{name: "provider error keeps finalizer", withFinalizer: true, deleteErr: errors.New("retry"), wantDeletes: 1, wantFinalizer: true, wantErr: true},
		{name: "confirmed deletion releases finalizer", withFinalizer: true, wantDeletes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testScheme(t)
			gateway := validGateway()
			gateway.DeletionTimestamp = &now
			if tt.withFinalizer {
				gateway.Finalizers = []string{GatewayFinalizer}
			} else {
				gateway.Finalizers = []string{"example.test/unrelated"}
			}
			provider := &fakeProvider{deleteErr: tt.deleteErr}
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gateway).Build()
			reconciler := &Reconciler{Client: client, Provider: provider}
			_, err := reconciler.Reconcile(context.Background(), requestFor(gateway))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %t", err, tt.wantErr)
			}
			if provider.deletes != tt.wantDeletes {
				t.Fatalf("provider deletes = %d, want %d", provider.deletes, tt.wantDeletes)
			}
			var got gatewayv1.Gateway
			getErr := client.Get(context.Background(), clientObjectKey(gateway), &got)
			if apierrors.IsNotFound(getErr) {
				if tt.wantFinalizer {
					t.Fatal("Gateway deleted while finalizer should remain")
				}
				return
			}
			if getErr != nil {
				t.Fatal(getErr)
			}
			if containsString(got.Finalizers, GatewayFinalizer) != tt.wantFinalizer {
				t.Fatalf("Gateway finalizers = %v, want retained %t", got.Finalizers, tt.wantFinalizer)
			}
		})
	}
}

func TestReconcileWithdrawsFinalOriginBeforeReleasingAssignment(t *testing.T) {
	scheme := testScheme(t)
	gateway := validGateway()
	backend := readyBackend()
	assignment := readyAssignment(backend, true)
	now := metav1.Now()
	assignment.DeletionTimestamp = &now
	assignment.Finalizers = []string{multiclusterbackend.OriginCleanupFinalizer}
	provider := &fakeProvider{}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &gatewayv1.GatewayClass{}, &fleetnetv1alpha1.MultiClusterBackend{}).
		WithObjects(acceptedGatewayClass(), gateway, attachedRoute(), backend, assignment).Build()
	reconciler := &Reconciler{Client: kubeClient, Provider: provider}

	if _, err := reconciler.Reconcile(context.Background(), requestFor(gateway)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if provider.withdraws != 1 || provider.reconciles != 0 {
		t.Fatalf("withdraws = %d, reconciles = %d; want withdrawal without empty programming", provider.withdraws, provider.reconciles)
	}
	var got fleetnetv1alpha1.ServiceOriginAssignment
	err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(assignment), &got)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err == nil && containsString(got.Finalizers, multiclusterbackend.OriginCleanupFinalizer) {
		t.Fatalf("assignment finalizers = %v, cleanup finalizer was not released", got.Finalizers)
	}
}

func TestGatewayClassReconcilerOwnsOnlyConfiguredClass(t *testing.T) {
	scheme := testScheme(t)
	class := acceptedGatewayClass()
	class.Status = gatewayv1.GatewayClassStatus{}
	other := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: ControllerName},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&gatewayv1.GatewayClass{}).WithObjects(class, other).Build()
	reconciler := &ClassReconciler{Client: kubeClient}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(class)}); err != nil {
		t.Fatal(err)
	}
	var got gatewayv1.GatewayClass
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(class), &got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, string(gatewayv1.GatewayClassConditionStatusAccepted)) {
		t.Fatalf("GatewayClass conditions = %#v", got.Status.Conditions)
	}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(other)}); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(other), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("unowned GatewayClass status was changed: %#v", got.Status.Conditions)
	}
}
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := fleetnetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func acceptedGatewayClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: string(GatewayClassName)},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: ControllerName},
		Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{{
			Type: string(gatewayv1.GatewayClassConditionStatusAccepted), Status: metav1.ConditionTrue,
		}}},
	}
}

func requestFor(gateway *gatewayv1.Gateway) reconcile.Request {
	return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)}
}

func clientObjectKey(object client.Object) client.ObjectKey {
	return client.ObjectKeyFromObject(object)
}

func validGateway() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "apps", Name: "global", UID: "gateway-uid", Generation: 3,
			Annotations: map[string]string{
				annotations.AFDSKUAnnotation:         string(annotations.SKUPremium),
				annotations.AFDWAFPolicyIDAnnotation: testWAFPolicyID,
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: GatewayClassName,
			Listeners:        []gatewayv1.Listener{{Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80}},
		},
	}
}

func attachedRoute() *gatewayv1.HTTPRoute {
	group := gatewayv1.Group(fleetnetv1alpha1.GroupVersion.Group)
	kind := gatewayv1.Kind(fleetnetv1alpha1.MultiClusterBackendKind)
	pathType := gatewayv1.PathMatchPathPrefix
	path := "/"
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "echo", Generation: 2},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "global"}}},
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &path}}},
				BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{Group: &group, Kind: &kind, Name: "echo"},
				}}},
			}},
		},
	}
}

func readyBackend() *fleetnetv1alpha1.MultiClusterBackend {
	return &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "echo", UID: "backend-uid", Generation: 1},
		Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
			Service:         fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 80},
			ClusterSelector: metav1.LabelSelector{MatchLabels: map[string]string{"environment": "poc"}},
		},
	}
}

func readyAssignment(backend *fleetnetv1alpha1.MultiClusterBackend, approved bool) *fleetnetv1alpha1.ServiceOriginAssignment {
	conditions := []metav1.Condition{{
		Type:   string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
		Status: metav1.ConditionTrue, Reason: "InfrastructureReady", ObservedGeneration: 1,
	}}
	if approved {
		conditions = append(conditions, metav1.Condition{
			Type:   string(fleetnetv1alpha1.ServiceOriginAssignmentConditionPrivateLinkApproved),
			Status: metav1.ConditionTrue, Reason: "ConnectionApproved", ObservedGeneration: 1,
		})
	}
	return &fleetnetv1alpha1.ServiceOriginAssignment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fleet-member-member-a", Name: "echo-a", UID: "assignment-uid", Generation: 1},
		Spec: fleetnetv1alpha1.ServiceOriginAssignmentSpec{
			BackendRef:   fleetnetv1alpha1.ServiceOriginAssignmentBackendReference{Namespace: backend.Namespace, Name: backend.Name, UID: backend.UID},
			ServiceRef:   fleetnetv1alpha1.ServiceOriginAssignmentServiceReference{Namespace: backend.Namespace, Name: "echo", Port: 80},
			Connectivity: fleetnetv1alpha1.ServiceOriginAssignmentConnectivity{Type: fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink},
			Approval:     fleetnetv1alpha1.ServiceOriginAssignmentApproval{RequestToken: "01234567890123456789012345678901"},
		},
		Status: fleetnetv1alpha1.ServiceOriginAssignmentStatus{
			ObservedGeneration: 1,
			Origin: &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
				AzureLocation: "eastus", LoadBalancerAddress: "10.0.0.4",
				PrivateLinkServiceID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/privateLinkServices/pls",
			},
			Conditions: conditions,
		},
	}
}
