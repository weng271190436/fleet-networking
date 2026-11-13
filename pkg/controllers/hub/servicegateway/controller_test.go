/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	fleetnet "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/common/hubconfig"
	azure "go.goms.io/fleet-networking/pkg/providers/azure/servicegateway"
)

type recordingProvider struct {
	withdrawn  bool
	reconciled bool
}

func (p *recordingProvider) Prepare(context.Context, string, azure.Config) (azure.Journal, error) {
	return azure.Journal{}, nil
}
func (p *recordingProvider) Reconcile(context.Context, string, azure.Journal, []azure.Application) (map[string]string, error) {
	p.reconciled = true
	return nil, nil
}
func (p *recordingProvider) Withdraw(context.Context, string, azure.Journal) error {
	p.withdrawn = true
	return nil
}

func controllerFixture(t *testing.T, objects ...client.Object) (*Reconciler, *recordingProvider) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = gatewayv1.Install(scheme)
	_ = gatewayv1alpha2.Install(scheme)
	_ = fleetnet.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gatewayv1.Gateway{}, &gatewayv1.GatewayClass{}, &gatewayv1alpha2.TCPRoute{}).WithObjects(objects...).Build()
	p := &recordingProvider{}
	return &Reconciler{Client: c, APIReader: c, Provider: p}, p
}

func TestServiceGatewayIgnoresAFDClass(t *testing.T) {
	g := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "test"}, Spec: gatewayv1.GatewaySpec{GatewayClassName: "afd"}}
	class := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "afd"}, Spec: gatewayv1.GatewayClassSpec{ControllerName: "networking.fleet.azure.com/afd"}}
	r, p := controllerFixture(t, g, class)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test", Name: "gateway"}})
	if err != nil || p.reconciled || p.withdrawn {
		t.Fatal("Service Gateway changed AFD behavior")
	}
}

func TestDeletionRestoresNetworkWithoutGatewayClass(t *testing.T) {
	journal, _ := json.Marshal(azure.Journal{})
	now := metav1.NewTime(time.Now())
	g := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "test", UID: "owner",
		Finalizers: []string{finalizer}, DeletionTimestamp: &now, Annotations: map[string]string{azure.JournalAnnotation: string(journal)}},
		Spec: gatewayv1.GatewaySpec{GatewayClassName: "deleted-class"}}
	r, p := controllerFixture(t, g)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	if err != nil || !p.withdrawn {
		t.Fatalf("deletion depends on deleted class: %v", err)
	}
}

func TestUnsupportedListenerDoesNotProgramAzure(t *testing.T) {
	r, p := controllerFixture(t)
	_, err := r.resolve(context.Background(), &gatewayv1.Gateway{Spec: gatewayv1.GatewaySpec{
		Listeners: []gatewayv1.Listener{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}}}})
	if err == nil || p.reconciled {
		t.Fatal("unsupported listener accepted")
	}
}

func TestResolveAggregatesTwoAcceptedMemberExports(t *testing.T) {
	g := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "apps", UID: "gateway-uid"},
		Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{{Name: "web", Port: 80, Protocol: gatewayv1.TCPProtocolType}}}}
	route := &gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: "route-uid"},
		Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "gateway", SectionName: ptr.To(gatewayv1.SectionName("web"))}}},
			Rules: []gatewayv1alpha2.TCPRouteRule{{BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
				Group: ptr.To(gatewayv1.Group(fleetnet.GroupVersion.Group)), Kind: ptr.To(gatewayv1.Kind("ServiceImport")), Name: "web", Port: ptr.To(gatewayv1.PortNumber(80))}}}}}}}
	ports := []fleetnet.ServicePort{{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromString("http")}}
	service := &fleetnet.ServiceImport{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", UID: "service-uid"},
		Status: fleetnet.ServiceImportStatus{Ports: ports, Clusters: []fleetnet.ClusterStatus{{Cluster: "a"}, {Cluster: "b"}}}}
	objects := []client.Object{g, route, service}
	for _, member := range []string{"a", "b"} {
		namespace := fmt.Sprintf(hubconfig.HubNamespaceNameFormat, member)
		podIP, nodeIP := "172.26.0.10", "172.26.0.5"
		if member == "b" {
			podIP, nodeIP = "172.26.4.10", "172.26.4.5"
		}
		objects = append(objects, &fleetnet.InternalServiceExport{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: namespace, Generation: 1},
			Spec: fleetnet.InternalServiceExportSpec{Type: corev1.ServiceTypeClusterIP, Ports: ports, ServiceReference: fleetnet.ExportedObjectReference{
				ClusterID: member, Namespace: "apps", Name: "web", NamespacedName: "apps/web", UID: types.UID(member)}},
			Status: fleetnet.InternalServiceExportStatus{Conditions: []metav1.Condition{{Type: "Conflict", Status: metav1.ConditionFalse, ObservedGeneration: 1}}}},
			&fleetnet.EndpointSliceExport{ObjectMeta: metav1.ObjectMeta{Name: "endpoints", Namespace: namespace},
				Spec: fleetnet.EndpointSliceExportSpec{EndpointSliceReference: fleetnet.ExportedObjectReference{ClusterID: member, Namespace: "apps", UID: types.UID(member + "-slice")},
					OwnerServiceReference: fleetnet.OwnerServiceReference{Namespace: "apps", Name: "web", NamespacedName: "apps/web"},
					Ports:                 []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To(int32(8080))}},
					ServiceGateway:        &fleetnet.ServiceGatewayEndpointSlice{ServiceUID: member, Endpoints: []fleetnet.ServiceGatewayEndpoint{{Addresses: []string{podIP}, NodeIP: nodeIP}}}}})
	}
	r, _ := controllerFixture(t, objects...)
	apps, err := r.resolve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || len(apps[0].Backends) != 2 || apps[0].BackendPort != 8080 {
		t.Fatalf("unexpected graph: %+v", apps)
	}
	// A named targetPort is resolved from actual EndpointSlice ports.
	if apps[0].FrontendPort == apps[0].BackendPort {
		t.Fatal("frontend port used as pod target port")
	}
}
