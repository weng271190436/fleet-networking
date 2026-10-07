/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package v1alpha1

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

var _ = Describe("Gateway backend API validation", func() {
	validMultiClusterBackend := func(name string) *fleetnetv1alpha1.MultiClusterBackend {
		return &fleetnetv1alpha1.MultiClusterBackend{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
			},
			Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
				Service: fleetnetv1alpha1.MultiClusterBackendService{
					Name: "echo",
					Port: 80,
				},
				ClusterSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"environment": "poc"},
				},
				HealthProbe: &fleetnetv1alpha1.MultiClusterBackendHealthProbe{},
			},
		}
	}

	validServiceOriginAssignment := func(name string) *fleetnetv1alpha1.ServiceOriginAssignment {
		return &fleetnetv1alpha1.ServiceOriginAssignment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
			},
			Spec: fleetnetv1alpha1.ServiceOriginAssignmentSpec{
				BackendRef: fleetnetv1alpha1.ServiceOriginAssignmentBackendReference{
					Namespace: "afd-private-demo",
					Name:      "echo",
					UID:       types.UID("2c66d893-0000-0000-0000-000000000000"),
				},
				ServiceRef: fleetnetv1alpha1.ServiceOriginAssignmentServiceReference{
					Namespace: "afd-private-demo",
					Name:      "echo",
					Port:      80,
				},
				Connectivity: fleetnetv1alpha1.ServiceOriginAssignmentConnectivity{
					Type: fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink,
				},
				Approval: fleetnetv1alpha1.ServiceOriginAssignmentApproval{
					RequestToken: "8384086c-0000-0000-0000-000000000000",
				},
			},
		}
	}

	Context("MultiClusterBackend", func() {
		It("creates a valid resource, defaults the probe path, and isolates status writes", func() {
			backend := validMultiClusterBackend("valid-backend")
			backend.Status.SelectedClusters = 1
			Expect(hubClient.Create(ctx, backend)).To(Succeed())
			DeferCleanup(func() {
				Expect(hubClient.Delete(ctx, backend)).To(Succeed())
			})

			var got fleetnetv1alpha1.MultiClusterBackend
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: backend.Namespace,
				Name:      backend.Name,
			}, &got)).To(Succeed())
			Expect(got.Spec.HealthProbe).NotTo(BeNil())
			Expect(got.Spec.HealthProbe.Path).NotTo(BeNil())
			Expect(*got.Spec.HealthProbe.Path).To(Equal("/"))
			Expect(got.Status.SelectedClusters).To(BeZero())

			got.Status.ObservedGeneration = got.Generation
			got.Status.SelectedClusters = 1
			got.Status.ReadyOrigins = 1
			got.Status.Members = []fleetnetv1alpha1.MultiClusterBackendMemberStatus{{
				ClusterName: "member-a",
			}}
			Expect(hubClient.Status().Update(ctx, &got)).To(Succeed())

			Eventually(func() (int32, error) {
				var statusUpdated fleetnetv1alpha1.MultiClusterBackend
				err := hubClient.Get(ctx, types.NamespacedName{
					Namespace: backend.Namespace,
					Name:      backend.Name,
				}, &statusUpdated)
				return statusUpdated.Status.SelectedClusters, err
			}).Should(Equal(int32(1)))
		})

		It("rejects invalid specs", func() {
			tests := []struct {
				name   string
				mutate func(*fleetnetv1alpha1.MultiClusterBackend)
			}{
				{
					name: "empty selector",
					mutate: func(backend *fleetnetv1alpha1.MultiClusterBackend) {
						backend.Spec.ClusterSelector = metav1.LabelSelector{}
					},
				},
				{
					name: "zero port",
					mutate: func(backend *fleetnetv1alpha1.MultiClusterBackend) {
						backend.Spec.Service.Port = 0
					},
				},
				{
					name: "port above 65535",
					mutate: func(backend *fleetnetv1alpha1.MultiClusterBackend) {
						backend.Spec.Service.Port = 65536
					},
				},
				{
					name: "relative health probe path",
					mutate: func(backend *fleetnetv1alpha1.MultiClusterBackend) {
						path := "healthz"
						backend.Spec.HealthProbe.Path = &path
					},
				},
			}

			for i, tt := range tests {
				backend := validMultiClusterBackend(fmt.Sprintf("invalid-backend-%d", i))
				tt.mutate(backend)
				Expect(hubClient.Create(ctx, backend)).ToNot(Succeed(), tt.name)
			}
		})

		It("rejects more than 40 member status entries", func() {
			backend := validMultiClusterBackend("too-many-member-statuses")
			Expect(hubClient.Create(ctx, backend)).To(Succeed())
			DeferCleanup(func() {
				Expect(hubClient.Delete(ctx, backend)).To(Succeed())
			})

			for i := 0; i < 41; i++ {
				backend.Status.Members = append(backend.Status.Members, fleetnetv1alpha1.MultiClusterBackendMemberStatus{
					ClusterName: fmt.Sprintf("member-%d", i),
				})
			}
			Expect(hubClient.Status().Update(ctx, backend)).ToNot(Succeed())
		})
	})

	Context("ServiceOriginAssignment", func() {
		It("creates a valid resource and isolates status writes", func() {
			assignment := validServiceOriginAssignment("valid-assignment")
			wantOrigin := &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
				AzureLocation:        "eastus",
				LoadBalancerAddress:  "10.0.1.4",
				PrivateLinkServiceID: "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/member/providers/Microsoft.Network/privateLinkServices/echo",
			}
			assignment.Status.Origin = wantOrigin
			Expect(hubClient.Create(ctx, assignment)).To(Succeed())
			DeferCleanup(func() {
				Expect(hubClient.Delete(ctx, assignment)).To(Succeed())
			})

			var got fleetnetv1alpha1.ServiceOriginAssignment
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: assignment.Namespace,
				Name:      assignment.Name,
			}, &got)).To(Succeed())
			Expect(got.Status.Origin).To(BeNil())

			got.Status.ObservedGeneration = got.Generation
			got.Status.Origin = wantOrigin
			Expect(hubClient.Status().Update(ctx, &got)).To(Succeed())

			Eventually(func() (string, error) {
				var statusUpdated fleetnetv1alpha1.ServiceOriginAssignment
				err := hubClient.Get(ctx, types.NamespacedName{
					Namespace: assignment.Namespace,
					Name:      assignment.Name,
				}, &statusUpdated)
				if err != nil || statusUpdated.Status.Origin == nil {
					return "", err
				}
				return statusUpdated.Status.Origin.PrivateLinkServiceID, nil
			}).Should(Equal(wantOrigin.PrivateLinkServiceID))
		})

		It("rejects invalid specs", func() {
			tests := []struct {
				name   string
				mutate func(*fleetnetv1alpha1.ServiceOriginAssignment)
			}{
				{
					name: "empty backend UID",
					mutate: func(assignment *fleetnetv1alpha1.ServiceOriginAssignment) {
						assignment.Spec.BackendRef.UID = ""
					},
				},
				{
					name: "zero service port",
					mutate: func(assignment *fleetnetv1alpha1.ServiceOriginAssignment) {
						assignment.Spec.ServiceRef.Port = 0
					},
				},
				{
					name: "unsupported connectivity",
					mutate: func(assignment *fleetnetv1alpha1.ServiceOriginAssignment) {
						assignment.Spec.Connectivity.Type = "Public"
					},
				},
				{
					name: "short request token",
					mutate: func(assignment *fleetnetv1alpha1.ServiceOriginAssignment) {
						assignment.Spec.Approval.RequestToken = "too-short"
					},
				},
				{
					name: "invalid request token characters",
					mutate: func(assignment *fleetnetv1alpha1.ServiceOriginAssignment) {
						assignment.Spec.Approval.RequestToken = "8384086c-0000-0000-0000-00000000000!"
					},
				},
			}

			for i, tt := range tests {
				assignment := validServiceOriginAssignment(fmt.Sprintf("invalid-assignment-%d", i))
				tt.mutate(assignment)
				Expect(hubClient.Create(ctx, assignment)).ToNot(Succeed(), tt.name)
			}
		})

		It("keeps the backend UID immutable", func() {
			assignment := validServiceOriginAssignment("immutable-backend-uid")
			Expect(hubClient.Create(ctx, assignment)).To(Succeed())
			DeferCleanup(func() {
				Expect(hubClient.Delete(ctx, assignment)).To(Succeed())
			})

			assignment.Spec.BackendRef.UID = types.UID("11111111-1111-1111-1111-111111111111")
			Expect(hubClient.Update(ctx, assignment)).ToNot(Succeed())
		})
	})
})
