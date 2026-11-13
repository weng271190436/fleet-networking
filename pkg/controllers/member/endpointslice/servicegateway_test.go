/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package endpointslice

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestServiceGatewayNodeMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}, Status: corev1.NodeStatus{
		Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.26.0.5"}},
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: "node"},
		Status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "172.26.0.10"}}}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "test", UID: "service-uid"}}
	r := &Reconciler{EnableServiceGatewayExport: true, MemberClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, pod, service).Build()}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "test",
		Labels:          map[string]string{discoveryv1.LabelServiceName: "web"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "web", UID: "service-uid"}}}, Endpoints: []discoveryv1.Endpoint{
		{Addresses: []string{"172.26.0.10"}, NodeName: ptr.To("node"), TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "pod", UID: "pod-uid"}},
		{Addresses: []string{"172.26.0.11"}, Conditions: discoveryv1.EndpointConditions{Terminating: ptr.To(true)}},
	}}
	result, err := r.serviceGatewayEndpoints(context.Background(), slice)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].NodeIP != "172.26.0.5" {
		t.Fatalf("unexpected metadata: %+v", result)
	}
	// Original MCS filtering and endpoint shape stay independent.
	if len(extractEndpointsFromEndpointSlice(slice)) != 2 {
		t.Fatal("changed MCS endpoint filtering")
	}
	slice.OwnerReferences[0].UID = "old-service"
	if _, err := r.serviceGatewayEndpoints(context.Background(), slice); err == nil {
		t.Fatal("stale Service UID was exported")
	}
	slice.OwnerReferences[0].UID = "service-uid"
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	if err := r.MemberClient.Status().Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	result, err = r.serviceGatewayEndpoints(context.Background(), slice)
	if err != nil || len(result.Endpoints) != 0 {
		t.Fatal("unready node remained in Service Gateway membership")
	}
	r.EnableServiceGatewayExport = false
	result, err = r.serviceGatewayEndpoints(context.Background(), slice)
	if err != nil || result != nil {
		t.Fatal("disabled metadata export changed behavior")
	}
}

func TestServiceGatewayMissingNodeFailsExplicitly(t *testing.T) {
	r := &Reconciler{EnableServiceGatewayExport: true}
	_, err := r.serviceGatewayEndpoints(context.Background(), &discoveryv1.EndpointSlice{
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"172.26.0.10"}}}})
	if err == nil {
		t.Fatal("missing node metadata silently accepted")
	}
}
