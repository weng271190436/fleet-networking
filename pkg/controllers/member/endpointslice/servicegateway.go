/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package endpointslice

import (
	"context"
	"fmt"
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

func (r *Reconciler) serviceGatewayEndpoints(ctx context.Context, slice *discoveryv1.EndpointSlice) (*fleetnetv1alpha1.ServiceGatewayEndpointSlice, error) {
	if !r.EnableServiceGatewayExport {
		return nil, nil
	}
	result := &fleetnetv1alpha1.ServiceGatewayEndpointSlice{
		Endpoints: []fleetnetv1alpha1.ServiceGatewayEndpoint{},
	}
	var service corev1.Service
	serviceName := slice.Labels[discoveryv1.LabelServiceName]
	if serviceName == "" {
		return nil, fmt.Errorf("EndpointSlice has no owner Service name")
	}
	if err := r.MemberClient.Get(ctx, client.ObjectKey{Namespace: slice.Namespace, Name: serviceName}, &service); err != nil {
		return nil, fmt.Errorf("read endpoint Service: %w", err)
	}
	matches := false
	for _, owner := range slice.OwnerReferences {
		matches = matches || (owner.Kind == "Service" && owner.UID == service.UID && owner.Name == service.Name)
	}
	if !matches {
		return nil, fmt.Errorf("EndpointSlice is not owned by the current Service UID")
	}
	result.ServiceUID = string(service.UID)
	for _, endpoint := range slice.Endpoints {
		if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready ||
			endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
			continue
		}
		if endpoint.NodeName == nil || *endpoint.NodeName == "" ||
			endpoint.TargetRef == nil || endpoint.TargetRef.Kind != "Pod" ||
			endpoint.TargetRef.Name == "" || endpoint.TargetRef.UID == "" {
			return nil, fmt.Errorf("Service Gateway requires pod endpoints with a node name")
		}
		var pod corev1.Pod
		if endpoint.TargetRef.Namespace != "" && endpoint.TargetRef.Namespace != slice.Namespace {
			return nil, fmt.Errorf("Service Gateway endpoint refers to a different namespace")
		}
		if err := r.MemberClient.Get(ctx, client.ObjectKey{Namespace: slice.Namespace, Name: endpoint.TargetRef.Name}, &pod); err != nil {
			return nil, fmt.Errorf("read endpoint pod: %w", err)
		}
		if pod.UID != endpoint.TargetRef.UID || pod.Spec.NodeName != *endpoint.NodeName {
			return nil, fmt.Errorf("endpoint pod UID or node location does not match")
		}
		var node corev1.Node
		if err := r.MemberClient.Get(ctx, client.ObjectKey{Name: *endpoint.NodeName}, &node); err != nil {
			return nil, fmt.Errorf("read endpoint node %s: %w", *endpoint.NodeName, err)
		}
		nodeReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				nodeReady = condition.Status == corev1.ConditionTrue
			}
		}
		if !nodeReady {
			continue
		}
		var nodeIP string
		for _, address := range node.Status.Addresses {
			ip, err := netip.ParseAddr(address.Address)
			if address.Type != corev1.NodeInternalIP || err != nil || !ip.Is4() {
				continue
			}
			if nodeIP != "" && nodeIP != ip.String() {
				return nil, fmt.Errorf("node %s has ambiguous IPv4 InternalIPs", node.Name)
			}
			nodeIP = ip.String()
		}
		if nodeIP == "" {
			return nil, fmt.Errorf("node %s has no IPv4 InternalIP", node.Name)
		}
		for _, address := range endpoint.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil || !ip.Is4() {
				return nil, fmt.Errorf("invalid IPv4 pod address %q", address)
			}
			found := false
			for _, podIP := range pod.Status.PodIPs {
				found = found || podIP.IP == address
			}
			if !found {
				return nil, fmt.Errorf("endpoint address %s is not assigned to pod %s", address, pod.Name)
			}
		}
		result.Endpoints = append(result.Endpoints, fleetnetv1alpha1.ServiceGatewayEndpoint{
			Addresses: append([]string(nil), endpoint.Addresses...), NodeIP: nodeIP,
		})
	}
	return result, nil
}

func (r *Reconciler) mapNodeEndpoints(ctx context.Context, object client.Object) []ctrl.Request {
	var slices discoveryv1.EndpointSliceList
	if err := r.MemberClient.List(ctx, &slices, client.MatchingFields{serviceGatewayNodeField: object.GetName()}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Could not list EndpointSlices for node-location update")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(slices.Items))
	for i := range slices.Items {
		requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&slices.Items[i])})
	}
	return requests
}

const serviceGatewayNodeField = ".serviceGatewayNode"
