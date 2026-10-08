/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package multiclusterbackend

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1beta1 "go.goms.io/fleet/apis/cluster/v1beta1"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

func TestReconcileSelectsMembersAndAggregatesStatus(t *testing.T) { //nolint:gocyclo // One lifecycle scenario intentionally checks all aggregated state.
	scheme := runtime.NewScheme()
	if err := fleetnetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(fleet networking) error = %v", err)
	}
	if err := clusterv1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(Fleet cluster) error = %v", err)
	}

	backend := validBackend()
	memberA := memberCluster("member-a", true, map[string]string{"environment": "poc"})
	memberB := memberCluster("member-b", true, map[string]string{"environment": "poc"})
	unjoined := memberCluster("member-unjoined", false, map[string]string{"environment": "poc"})
	nonmatching := memberCluster("member-nonmatching", true, map[string]string{"environment": "production"})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&fleetnetv1alpha1.MultiClusterBackend{}, &fleetnetv1alpha1.ServiceOriginAssignment{}).
		WithObjects(backend, memberB, unjoined, memberA, nonmatching).
		Build()

	tokenCounter := 0
	reconciler := &Reconciler{
		Client: k8sClient,
		NewRequestToken: func() (string, error) {
			tokenCounter++
			return fmt.Sprintf("%032d", tokenCounter), nil
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: backend.Namespace, Name: backend.Name}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	assignments := listAssignments(t, k8sClient)
	if len(assignments) != 2 {
		t.Fatalf("assignment count = %d, want 2", len(assignments))
	}
	wantNamespaces := map[string]bool{
		"fleet-member-member-a": true,
		"fleet-member-member-b": true,
	}
	originalTokens := make(map[types.NamespacedName]string, len(assignments))
	for i := range assignments {
		assignment := &assignments[i]
		if !wantNamespaces[assignment.Namespace] {
			t.Errorf("assignment namespace = %q, want a selected member namespace", assignment.Namespace)
		}
		if assignment.Spec.BackendRef.UID != backend.UID {
			t.Errorf("backend UID = %q, want %q", assignment.Spec.BackendRef.UID, backend.UID)
		}
		if assignment.Spec.ServiceRef.Name != backend.Spec.Service.Name ||
			assignment.Spec.ServiceRef.Port != backend.Spec.Service.Port {
			t.Errorf("service ref = %#v, want backend service %#v", assignment.Spec.ServiceRef, backend.Spec.Service)
		}
		originalTokens[types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name}] = assignment.Spec.Approval.RequestToken
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	assignments = listAssignments(t, k8sClient)
	if len(assignments) != 2 {
		t.Fatalf("assignment count after second reconcile = %d, want 2", len(assignments))
	}
	for i := range assignments {
		assignment := &assignments[i]
		key := types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name}
		if assignment.Spec.Approval.RequestToken != originalTokens[key] {
			t.Errorf("request token changed for %s", key)
		}
	}

	var readyAssignment *fleetnetv1alpha1.ServiceOriginAssignment
	for i := range assignments {
		if assignments[i].Namespace == "fleet-member-member-a" {
			readyAssignment = assignments[i].DeepCopy()
			break
		}
	}
	if readyAssignment == nil {
		t.Fatal("member-a assignment not found")
	}
	readyAssignment.Status.Conditions = []metav1.Condition{
		{
			Type:               string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
			Status:             metav1.ConditionTrue,
			Reason:             string(fleetnetv1alpha1.ServiceOriginAssignmentReasonInfrastructureReady),
			ObservedGeneration: readyAssignment.Generation,
		},
	}
	if err := k8sClient.Status().Update(context.Background(), readyAssignment); err != nil {
		t.Fatalf("Status().Update(assignment) error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() after status update error = %v", err)
	}

	var gotBackend fleetnetv1alpha1.MultiClusterBackend
	if err := k8sClient.Get(context.Background(), req.NamespacedName, &gotBackend); err != nil {
		t.Fatalf("Get(backend) error = %v", err)
	}
	if gotBackend.Status.SelectedClusters != 2 {
		t.Errorf("SelectedClusters = %d, want 2", gotBackend.Status.SelectedClusters)
	}
	if gotBackend.Status.ReadyOrigins != 1 {
		t.Errorf("ReadyOrigins = %d, want 1", gotBackend.Status.ReadyOrigins)
	}
	if len(gotBackend.Status.Members) != 2 ||
		gotBackend.Status.Members[0].ClusterName != "member-a" ||
		gotBackend.Status.Members[1].ClusterName != "member-b" {
		t.Errorf("member status = %#v, want sorted member-a and member-b", gotBackend.Status.Members)
	}
	accepted := meta.FindStatusCondition(gotBackend.Status.Conditions, string(fleetnetv1alpha1.MultiClusterBackendConditionAccepted))
	if accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Errorf("Accepted condition = %#v, want True", accepted)
	}

	memberA.Labels["environment"] = "production"
	if err := k8sClient.Update(context.Background(), memberA); err != nil {
		t.Fatalf("Update(member-a) error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() after label change error = %v", err)
	}
	assignments = listAssignments(t, k8sClient)
	if len(assignments) != 2 {
		t.Fatalf("assignment count during withdrawal = %d, want 2", len(assignments))
	}
	for i := range assignments {
		if assignments[i].Namespace == "fleet-member-member-a" {
			if assignments[i].DeletionTimestamp.IsZero() {
				t.Error("member-a assignment is not marked for deletion")
			}
			withdrawing := assignments[i].DeepCopy()
			withdrawing.Status.Conditions = []metav1.Condition{{
				Type:               string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
				Status:             metav1.ConditionFalse,
				Reason:             string(fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkServiceNotReady),
				ObservedGeneration: withdrawing.Generation,
			}}
			if err := k8sClient.Status().Update(context.Background(), withdrawing); err != nil {
				t.Fatalf("Status().Update(withdrawing assignment) error = %v", err)
			}
		}
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() after origin withdrawal error = %v", err)
	}
	assignments = listAssignments(t, k8sClient)
	if len(assignments) != 1 || assignments[0].Namespace != "fleet-member-member-b" {
		t.Errorf("assignments after origin withdrawal = %#v, want only member-b", assignments)
	}

	memberB.Labels["environment"] = "production"
	if err := k8sClient.Update(context.Background(), memberB); err != nil {
		t.Fatalf("Update(member-b) error = %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() after complete deselection error = %v", err)
	}
	if assignments := listAssignments(t, k8sClient); len(assignments) != 0 {
		t.Errorf("assignment count after complete deselection = %d, want 0", len(assignments))
	}
}

func TestReconcileRejectsOverLimitSelection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := fleetnetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(fleet networking) error = %v", err)
	}
	if err := clusterv1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(Fleet cluster) error = %v", err)
	}

	backend := validBackend()
	objects := make([]runtime.Object, 0, fleetnetv1alpha1.MultiClusterBackendSelectedMemberLimit+2)
	objects = append(objects, backend)
	for i := 0; i <= fleetnetv1alpha1.MultiClusterBackendSelectedMemberLimit; i++ {
		objects = append(objects, memberCluster(fmt.Sprintf("member-%02d", i), true, map[string]string{"environment": "poc"}))
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&fleetnetv1alpha1.MultiClusterBackend{}, &fleetnetv1alpha1.ServiceOriginAssignment{}).
		WithRuntimeObjects(objects...).
		Build()
	reconciler := &Reconciler{Client: k8sClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: backend.Namespace, Name: backend.Name}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if assignments := listAssignments(t, k8sClient); len(assignments) != 0 {
		t.Errorf("assignment count = %d, want 0", len(assignments))
	}
	var got fleetnetv1alpha1.MultiClusterBackend
	if err := k8sClient.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatalf("Get(backend) error = %v", err)
	}
	accepted := meta.FindStatusCondition(got.Status.Conditions, string(fleetnetv1alpha1.MultiClusterBackendConditionAccepted))
	if accepted == nil ||
		accepted.Status != metav1.ConditionFalse ||
		accepted.Reason != string(fleetnetv1alpha1.MultiClusterBackendReasonTooManySelectedClusters) {
		t.Errorf("Accepted condition = %#v, want TooManySelectedClusters", accepted)
	}
}

func validBackend() *fleetnetv1alpha1.MultiClusterBackend {
	return &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "echo",
			Namespace: "afd-private-demo",
			UID:       types.UID("2c66d893-0000-0000-0000-000000000000"),
		},
		Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
			Service: fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 80},
			ClusterSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"environment": "poc"},
			},
		},
	}
}

func memberCluster(name string, joined bool, labels map[string]string) *clusterv1beta1.MemberCluster {
	status := metav1.ConditionFalse
	if joined {
		status = metav1.ConditionTrue
	}
	return &clusterv1beta1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: clusterv1beta1.MemberClusterStatus{
			Conditions: []metav1.Condition{{
				Type:   string(clusterv1beta1.ConditionTypeMemberClusterJoined),
				Status: status,
				Reason: "Test",
			}},
		},
	}
}

func listAssignments(t *testing.T, k8sClient client.Client) []fleetnetv1alpha1.ServiceOriginAssignment {
	t.Helper()
	var assignments fleetnetv1alpha1.ServiceOriginAssignmentList
	if err := k8sClient.List(context.Background(), &assignments); err != nil {
		t.Fatalf("List(assignments) error = %v", err)
	}
	return assignments.Items
}
