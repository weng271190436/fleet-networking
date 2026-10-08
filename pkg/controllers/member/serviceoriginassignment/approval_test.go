/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

const (
	testAssignmentUID  = types.UID("11111111-1111-1111-1111-111111111111")
	testRequestToken   = "0123456789abcdef0123456789abcdef"
	testRequesterSub   = "22222222-2222-2222-2222-222222222222"
	testRequestMessage = "fleet:11111111-1111-1111-1111-111111111111:" +
		"0123456789abcdef0123456789abcdef"
	testManagedPrivateEndpointID = "/subscriptions/" + testRequesterSub +
		"/resourceGroups/afd/providers/Microsoft.Network/privateEndpoints/origin"
)

func TestFindApprovalConnection(t *testing.T) {
	tests := []struct {
		name         string
		plsID        string
		actualPLSID  string
		connections  []*armnetwork.PrivateEndpointConnection
		allowlist    map[string]struct{}
		wantName     string
		wantApproved bool
		wantErr      string
	}{
		{
			name:  "one exact pending connection",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("unrelated", "Pending", "other", testManagedPrivateEndpointID),
				testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
			},
			wantName: "expected",
		},
		{
			name:        "PLS ID mismatch fails closed",
			plsID:       testPLSID,
			actualPLSID: testPLSID + "-other",
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
			},
			wantErr: "changed",
		},
		{
			name:  "already approved",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Approved", testRequestMessage, testManagedPrivateEndpointID),
			},
			wantName:     "expected",
			wantApproved: true,
		},
		{
			name:  "token mismatch replay is unrelated",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("old", "Pending", "fleet:"+string(testAssignmentUID)+":old-token", testManagedPrivateEndpointID),
			},
		},
		{
			name:  "substring message is unrelated",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("partial", "Pending", "prefix-"+testRequestMessage, testManagedPrivateEndpointID),
			},
		},
		{
			name:  "multiple exact matches fail closed",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("first", "Pending", testRequestMessage, testManagedPrivateEndpointID),
				testConnection("second", "Pending", testRequestMessage, testManagedPrivateEndpointID),
			},
			wantErr: "multiple",
		},
		{
			name:  "non-pending exact match fails closed",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("rejected", "Rejected", testRequestMessage, testManagedPrivateEndpointID),
			},
			wantErr: "Rejected",
		},
		{
			name:  "allowlisted subscription",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
			},
			allowlist: map[string]struct{}{testRequesterSub: {}},
			wantName:  "expected",
		},
		{
			name:  "missing managed endpoint ID fails closed",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, ""),
			},
			allowlist: map[string]struct{}{testRequesterSub: {}},
			wantErr:   "managed private endpoint",
		},
		{
			name:  "malformed managed endpoint ID fails closed",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, "/not/an/azure/resource"),
			},
			allowlist: map[string]struct{}{testRequesterSub: {}},
			wantErr:   "managed private endpoint",
		},
		{
			name:  "unallowlisted subscription fails closed",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
			},
			allowlist: map[string]struct{}{"33333333-3333-3333-3333-333333333333": {}},
			wantErr:   "not allowlisted",
		},
		{
			name:  "empty allowlist skips missing endpoint check",
			plsID: testPLSID,
			connections: []*armnetwork.PrivateEndpointConnection{
				testConnection("expected", "Pending", testRequestMessage, ""),
			},
			wantName: "expected",
		},
	}

	assignment := testAssignment()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actualPLSID := tc.actualPLSID
			if actualPLSID == "" {
				actualPLSID = testPLSID
			}
			connection, approved, err := findApprovalConnection(
				assignment, tc.plsID, actualPLSID, tc.connections, tc.allowlist)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("findApprovalConnection() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("findApprovalConnection() error = %v", err)
			}
			if tc.wantName == "" {
				if connection != nil {
					t.Fatalf("findApprovalConnection() = %q, want nil", ptr.Deref(connection.Name, ""))
				}
				return
			}
			if connection == nil || ptr.Deref(connection.Name, "") != tc.wantName {
				t.Fatalf("findApprovalConnection() = %#v, want %q", connection, tc.wantName)
			}
			if approved != tc.wantApproved {
				t.Errorf("approved = %t, want %t", approved, tc.wantApproved)
			}
		})
	}
}

func TestParseRequesterSubscriptionAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantLen int
		wantErr bool
	}{
		{name: "empty", value: "", wantLen: 0},
		{name: "normalizes and deduplicates", value: strings.ToUpper(testRequesterSub) + "," + testRequesterSub, wantLen: 1},
		{name: "trims whitespace", value: "  " + testRequesterSub + "  ", wantLen: 1},
		{name: "rejects malformed UUID", value: "not-a-uuid", wantErr: true},
		{name: "rejects empty entry", value: testRequesterSub + ",", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRequesterSubscriptionAllowlist(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseRequesterSubscriptionAllowlist() error = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && len(got) != tc.wantLen {
				t.Errorf("len(allowlist) = %d, want %d", len(got), tc.wantLen)
			}
			if err == nil && tc.wantLen > 0 {
				if _, ok := got[testRequesterSub]; !ok {
					t.Errorf("normalized subscription %q not found in %#v", testRequesterSub, got)
				}
			}
		})
	}
}

func TestReconcileApprovesOnlyRevalidatedConnection(t *testing.T) {
	assignment := testAssignment()
	memberClient := newReadyMemberClient(t)
	hubClient := newCountingHubClient(t, assignment)
	connections := &fakePrivateEndpointConnectionClient{
		listItems: []*armnetwork.PrivateEndpointConnection{
			testConnection("unrelated", "Pending", "other", testManagedPrivateEndpointID),
			testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
		},
	}
	reconciler := readyApprovalReconciler(hubClient, memberClient, connections)

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(connections.approvedNames) != 1 || connections.approvedNames[0] != "expected" {
		t.Fatalf("approved connections = %#v, want [expected]", connections.approvedNames)
	}
	got := getAssignment(t, hubClient.Client, assignment)
	assertCondition(t, got.Status.Conditions,
		fleetnetv1alpha1.ServiceOriginAssignmentConditionPrivateLinkApproved,
		metav1.ConditionTrue, fleetnetv1alpha1.ServiceOriginAssignmentReasonConnectionApproved)
}

func TestReconcileObservesAlreadyApprovedConnection(t *testing.T) {
	assignment := testAssignment()
	memberClient := newReadyMemberClient(t)
	hubClient := newCountingHubClient(t, assignment)
	connections := &fakePrivateEndpointConnectionClient{
		listItems: []*armnetwork.PrivateEndpointConnection{
			testConnection("expected", "Approved", testRequestMessage, testManagedPrivateEndpointID),
		},
	}
	reconciler := readyApprovalReconciler(hubClient, memberClient, connections)

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(connections.approvedNames) != 0 {
		t.Fatalf("approved connections = %#v, want no update", connections.approvedNames)
	}
	got := getAssignment(t, hubClient.Client, assignment)
	assertCondition(t, got.Status.Conditions,
		fleetnetv1alpha1.ServiceOriginAssignmentConditionPrivateLinkApproved,
		metav1.ConditionTrue, fleetnetv1alpha1.ServiceOriginAssignmentReasonConnectionApproved)
}

func TestReconcileApprovalFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*fleetnetv1alpha1.ServiceOriginAssignment, *coreFixture, *fakePrivateEndpointConnectionClient)
		wantStatus metav1.ConditionStatus
		wantReason fleetnetv1alpha1.ServiceOriginAssignmentConditionReason
		wantErr    bool
	}{
		{
			name: "no matching connection remains pending",
			mutate: func(_ *fleetnetv1alpha1.ServiceOriginAssignment, _ *coreFixture, c *fakePrivateEndpointConnectionClient) {
				c.listItems = []*armnetwork.PrivateEndpointConnection{testConnection("other", "Pending", "other", testManagedPrivateEndpointID)}
			},
			wantStatus: metav1.ConditionUnknown,
			wantReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonConnectionPending,
		},
		{
			name: "azure list error is reported",
			mutate: func(_ *fleetnetv1alpha1.ServiceOriginAssignment, _ *coreFixture, c *fakePrivateEndpointConnectionClient) {
				c.listErr = errors.New("list unavailable")
			},
			wantStatus: metav1.ConditionUnknown,
			wantReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkApprovalFailed,
			wantErr:    true,
		},
		{
			name: "azure approval error is reported",
			mutate: func(_ *fleetnetv1alpha1.ServiceOriginAssignment, _ *coreFixture, c *fakePrivateEndpointConnectionClient) {
				c.approveErr = errors.New("update unavailable")
			},
			wantStatus: metav1.ConditionUnknown,
			wantReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkApprovalFailed,
			wantErr:    true,
		},
		{
			name: "azure revalidation get error is reported",
			mutate: func(_ *fleetnetv1alpha1.ServiceOriginAssignment, _ *coreFixture, c *fakePrivateEndpointConnectionClient) {
				c.getErr = errors.New("get unavailable")
			},
			wantStatus: metav1.ConditionUnknown,
			wantReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonPrivateLinkApprovalFailed,
			wantErr:    true,
		},
		{
			name: "terminating assignment is not approved",
			mutate: func(a *fleetnetv1alpha1.ServiceOriginAssignment, _ *coreFixture, _ *fakePrivateEndpointConnectionClient) {
				now := metav1.Now()
				a.DeletionTimestamp = &now
				a.Finalizers = []string{"test"}
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: fleetnetv1alpha1.ServiceOriginAssignmentReasonApprovalValidationFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assignment := testAssignment()
			fixture := &coreFixture{memberClient: newReadyMemberClient(t)}
			connections := &fakePrivateEndpointConnectionClient{
				listItems: []*armnetwork.PrivateEndpointConnection{
					testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
				},
			}
			tc.mutate(assignment, fixture, connections)
			hubClient := newCountingHubClient(t, assignment)
			reconciler := readyApprovalReconciler(hubClient, fixture.memberClient, connections)
			_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %t", err, tc.wantErr)
			}
			if len(connections.approvedNames) != 0 {
				t.Fatalf("approved connections = %#v, want none", connections.approvedNames)
			}
			got := getAssignment(t, hubClient.Client, assignment)
			assertCondition(t, got.Status.Conditions,
				fleetnetv1alpha1.ServiceOriginAssignmentConditionPrivateLinkApproved,
				tc.wantStatus, tc.wantReason)
		})
	}
}

func TestReconcileRevalidatesImmediatelyBeforeApproval(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*countingHubClient, client.Client, *fakePrivateLinkServiceClient)
	}{
		{
			name: "assignment deleted",
			mutate: func(hub *countingHubClient, _ client.Client, _ *fakePrivateLinkServiceClient) {
				assignment := testAssignment()
				if err := hub.Client.Delete(context.Background(), assignment); err != nil {
					t.Fatalf("Delete(assignment) error = %v", err)
				}
			},
		},
		{
			name: "assignment generation changed",
			mutate: func(hub *countingHubClient, _ client.Client, _ *fakePrivateLinkServiceClient) {
				assignment := getAssignment(t, hub.Client, testAssignment())
				assignment.Generation++
				if err := hub.Client.Update(context.Background(), assignment); err != nil {
					t.Fatalf("Update(assignment) error = %v", err)
				}
			},
		},
		{
			name: "assignment discovery status became stale",
			mutate: func(hub *countingHubClient, _ client.Client, _ *fakePrivateLinkServiceClient) {
				assignment := getAssignment(t, hub.Client, testAssignment())
				assignment.Status.ObservedGeneration--
				if err := hub.Client.Status().Update(context.Background(), assignment); err != nil {
					t.Fatalf("Status().Update(assignment) error = %v", err)
				}
			},
		},
		{
			name: "local Service deleted",
			mutate: func(_ *countingHubClient, member client.Client, _ *fakePrivateLinkServiceClient) {
				if err := member.Delete(context.Background(), testService(corev1.ServiceTypeLoadBalancer, true, true, 80)); err != nil {
					t.Fatalf("Delete(Service) error = %v", err)
				}
			},
		},
		{
			name: "PLS discovery changed",
			mutate: func(_ *countingHubClient, _ client.Client, pls *fakePrivateLinkServiceClient) {
				changed := matchingPrivateLinkServices()
				changed[0].ID = ptr.To(testPLSID + "-changed")
				pls.items = changed
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assignment := testAssignment()
			memberClient := newReadyMemberClient(t)
			hubClient := newCountingHubClient(t, assignment)
			plsClient := &fakePrivateLinkServiceClient{items: matchingPrivateLinkServices()}
			connections := &fakePrivateEndpointConnectionClient{
				listItems: []*armnetwork.PrivateEndpointConnection{
					testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID),
				},
			}
			connections.listHook = func() {
				tc.mutate(hubClient, memberClient, plsClient)
			}
			reconciler := readyApprovalReconciler(hubClient, memberClient, connections)
			reconciler.PrivateLinkServiceClient = plsClient

			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: assignment.Namespace, Name: assignment.Name},
			}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if len(connections.approvedNames) != 0 {
				t.Fatalf("approved connections = %#v, want none", connections.approvedNames)
			}
		})
	}
}

type coreFixture struct {
	memberClient client.Client
}

type fakePrivateEndpointConnectionClient struct {
	listItems     []*armnetwork.PrivateEndpointConnection
	listErr       error
	getErr        error
	approveErr    error
	approvedNames []string
	listHook      func()
}

func (f *fakePrivateEndpointConnectionClient) List(_ context.Context, _ string) ([]*armnetwork.PrivateEndpointConnection, error) {
	if f.listHook != nil {
		f.listHook()
	}
	return f.listItems, f.listErr
}

func (f *fakePrivateEndpointConnectionClient) Get(_ context.Context, _ string, name string) (*armnetwork.PrivateEndpointConnection, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, connection := range f.listItems {
		if ptr.Deref(connection.Name, "") == name {
			return connection, nil
		}
	}
	return nil, errors.New("connection not found")
}

func (f *fakePrivateEndpointConnectionClient) Approve(
	_ context.Context, _ string, connection *armnetwork.PrivateEndpointConnection,
) error {
	if f.approveErr != nil {
		return f.approveErr
	}
	f.approvedNames = append(f.approvedNames, ptr.Deref(connection.Name, ""))
	return nil
}

func readyApprovalReconciler(
	hubClient client.Client,
	memberClient client.Client,
	connectionClient PrivateEndpointConnectionClient,
) *Reconciler {
	return &Reconciler{
		HubClient:                       hubClient,
		MemberClient:                    memberClient,
		ResourceGroupName:               testResourceGroup,
		LoadBalancerClient:              &fakeLoadBalancerClient{items: matchingLoadBalancers()},
		PrivateLinkServiceClient:        &fakePrivateLinkServiceClient{items: matchingPrivateLinkServices()},
		PrivateEndpointConnectionClient: connectionClient,
		RequesterSubscriptionAllowlist:  map[string]struct{}{testRequesterSub: {}},
	}
}

func newReadyMemberClient(t *testing.T) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testService(corev1.ServiceTypeLoadBalancer, true, true, 80)).
		Build()
}

func testConnection(name, status, message, privateEndpointID string) *armnetwork.PrivateEndpointConnection {
	connection := &armnetwork.PrivateEndpointConnection{
		Name: ptr.To(name),
		Properties: &armnetwork.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armnetwork.PrivateLinkServiceConnectionState{
				Status:      ptr.To(status),
				Description: ptr.To(message),
			},
		},
	}
	if privateEndpointID != "" {
		connection.Properties.PrivateEndpoint = &armnetwork.PrivateEndpoint{ID: ptr.To(privateEndpointID)}
	}
	return connection
}
