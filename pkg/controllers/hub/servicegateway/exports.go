/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"fmt"
	"reflect"
	"sort"

	fleetnet "go.goms.io/fleet-networking/api/v1alpha1"
)

// A reannotation race can leave multiple transport objects for one source UID.
// Only its latest generation is authoritative; unioning them revives old pods.
func currentEndpointExports(exports []fleetnet.EndpointSliceExport) ([]fleetnet.EndpointSliceExport, error) {
	type key struct{ namespace, cluster, sourceNamespace, uid string }
	selected := map[key]fleetnet.EndpointSliceExport{}
	// Generation order ensures an obsolete contradictory pair cannot veto a
	// newer authoritative source snapshot.
	sort.SliceStable(exports, func(i, j int) bool {
		return exports[i].Spec.EndpointSliceReference.Generation > exports[j].Spec.EndpointSliceReference.Generation
	})
	for _, export := range exports {
		if export.DeletionTimestamp != nil {
			continue
		}
		source := export.Spec.EndpointSliceReference
		k := key{export.Namespace, source.ClusterID, source.Namespace, string(source.UID)}
		previous, exists := selected[k]
		if !exists {
			selected[k] = export
			continue
		}
		if previous.Spec.EndpointSliceReference.Generation != source.Generation {
			continue
		}
		a, b := previous.Spec, export.Spec
		a.EndpointSliceReference = fleetnet.ExportedObjectReference{}
		b.EndpointSliceReference = fleetnet.ExportedObjectReference{}
		if !reflect.DeepEqual(a, b) {
			return nil, fmt.Errorf("conflicting endpoint snapshots for member %s source UID %s generation %d", source.ClusterID, source.UID, source.Generation)
		}
	}
	result := make([]fleetnet.EndpointSliceExport, 0, len(selected))
	for _, export := range selected {
		result = append(result, export)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
