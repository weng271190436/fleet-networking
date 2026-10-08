/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package gatewaymodel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

const maxAFDChildResourceNameLength = 50

// MultiClusterBackend is the provider-neutral desired state for one Fleet backend.
type MultiClusterBackend struct {
	Namespace       string
	Name            string
	UID             string
	Port            int32
	HealthProbePath string
	OriginGroupName string
	Origins         []Origin
}

// BuildMultiClusterBackend resolves ready assignment status into deterministic private origins.
func BuildMultiClusterBackend(
	backend *fleetnetv1alpha1.MultiClusterBackend,
	assignments []fleetnetv1alpha1.ServiceOriginAssignment,
) (MultiClusterBackend, error) {
	if backend == nil {
		return MultiClusterBackend{}, fmt.Errorf("MultiClusterBackend must not be nil")
	}

	result := MultiClusterBackend{
		Namespace:       backend.Namespace,
		Name:            backend.Name,
		UID:             string(backend.UID),
		Port:            backend.Spec.Service.Port,
		HealthProbePath: "/",
		OriginGroupName: deterministicName("og", backend.Name, backend.Namespace+"/"+backend.Name+"/"+string(backend.UID)),
	}
	if backend.Spec.HealthProbe != nil && backend.Spec.HealthProbe.Path != nil {
		result.HealthProbePath = *backend.Spec.HealthProbe.Path
	}

	for i := range assignments {
		assignment := &assignments[i]
		if assignment.Spec.BackendRef.Namespace != backend.Namespace ||
			assignment.Spec.BackendRef.Name != backend.Name ||
			assignment.Spec.BackendRef.UID != backend.UID ||
			assignment.Status.ObservedGeneration != assignment.Generation ||
			!meta.IsStatusConditionTrue(assignment.Status.Conditions, string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady)) {
			continue
		}
		if assignment.Spec.Connectivity.Type != fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink {
			return MultiClusterBackend{}, fmt.Errorf("ready ServiceOriginAssignment %s/%s does not require Private Link", assignment.Namespace, assignment.Name)
		}
		if assignment.Status.Origin == nil ||
			assignment.Status.Origin.LoadBalancerAddress == "" ||
			assignment.Status.Origin.AzureLocation == "" ||
			assignment.Status.Origin.PrivateLinkServiceID == "" {
			return MultiClusterBackend{}, fmt.Errorf("ready ServiceOriginAssignment %s/%s has incomplete origin status", assignment.Namespace, assignment.Name)
		}
		cluster := assignmentCluster(assignment)
		result.Origins = append(result.Origins, Origin{
			Cluster:               cluster,
			Name:                  deterministicName("origin", cluster, string(assignment.UID)),
			Endpoint:              assignment.Status.Origin.LoadBalancerAddress,
			Weight:                maxServiceExportWeight,
			Connectivity:          string(fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink),
			PrivateLinkResourceID: assignment.Status.Origin.PrivateLinkServiceID,
			PrivateLinkLocation:   assignment.Status.Origin.AzureLocation,
			RequestMessage:        fmt.Sprintf("fleet:%s:%s", assignment.UID, assignment.Spec.Approval.RequestToken),
		})
	}
	sort.Slice(result.Origins, func(i, j int) bool {
		return result.Origins[i].Cluster < result.Origins[j].Cluster
	})
	return result, nil
}

func assignmentCluster(assignment *fleetnetv1alpha1.ServiceOriginAssignment) string {
	const prefix = "fleet-member-"
	if len(assignment.Namespace) > len(prefix) && assignment.Namespace[:len(prefix)] == prefix {
		return assignment.Namespace[len(prefix):]
	}
	return assignment.Namespace
}

func deterministicName(prefix, readable, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	hash := hex.EncodeToString(sum[:4])
	readable = sanitizeAFDNamePart(readable)
	maxReadableLength := maxAFDChildResourceNameLength - len(prefix) - len(hash) - 2
	if len(readable) > maxReadableLength {
		readable = strings.Trim(readable[:maxReadableLength], "-")
	}
	if readable == "" {
		readable = "resource"
	}
	return fmt.Sprintf("%s-%s-%s", prefix, readable, hash)
}

func sanitizeAFDNamePart(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	lastDash := false
	for _, char := range strings.ToLower(value) {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			result.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}
