//go:build e2e

/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	fleetnet "go.goms.io/fleet-networking/api/v1alpha1"
	"go.goms.io/fleet-networking/pkg/common/azureerrors"
	sg "go.goms.io/fleet-networking/pkg/providers/azure/servicegateway"
	"go.goms.io/fleet-networking/test/e2e/framework"
)

const serviceGatewayProofTimeout = 30 * time.Minute

var _ = Describe("Service Gateway TCP proof", Serial, Label("service-gateway-proof"), func() {
	It("proves exported cross-member delivery, readiness withdrawal and ordered network cleanup", func(ctx SpecContext) {
		if os.Getenv("ENABLE_SERVICE_GATEWAY_PROOF") != "true" {
			Skip("Explicit ENABLE_SERVICE_GATEWAY_PROOF=true and approved subnet cutover are required")
		}
		Expect(enabledNetworkingFeatures).To(BeTrue())
		Expect(memberClusters).To(HaveLen(2))
		Expect(os.Getenv("RETAIN_AFD_E2E_SETUP")).NotTo(Equal("true"), "Run this proof independently from the AFD retained diagnostic")
		configFile := os.Getenv("SERVICE_GATEWAY_PROOF_CONFIG")
		Expect(configFile).NotTo(BeEmpty(), "Provide an approved JSON configuration file")
		data, err := os.ReadFile(configFile)
		Expect(err).NotTo(HaveOccurred())
		config, err := sg.ParseConfig(string(data))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.HasPrefix(strings.ToLower(config.GatewayID), "/subscriptions/"+strings.ToLower(subscriptionID)+"/")).To(BeTrue())
		credential, err := azidentity.NewDefaultAzureCredential(nil)
		Expect(err).NotTo(HaveOccurred())
		api, err := sg.NewAPI(credential, nil)
		Expect(err).NotTo(HaveOccurred())

		By("requiring fresh Azure resource identities and initially unassociated subnets")
		roots := []string{config.GatewayID, config.NATGatewayID, config.EgressPublicIPID}
		for _, id := range roots {
			_, err := api.Get(ctx, id)
			Expect(azureerrors.IsNotFound(err)).To(BeTrue(), "Pre-existing resource is not controller creation evidence: %s (%v)", id, err)
		}
		for _, subnet := range config.SubnetIDs {
			resource, err := api.Get(ctx, subnet)
			Expect(err).NotTo(HaveOccurred())
			var association *struct {
				ID string `json:"id"`
			}
			if raw := resource.Properties["serviceGateway"]; len(raw) != 0 {
				Expect(json.Unmarshal(raw, &association)).To(Succeed())
			}
			Expect(association == nil || association.ID == "").To(BeTrue(), "Do not adopt an already linked subnet")
		}
		for _, member := range memberClusters {
			Expect(serviceGatewayNodesReady(ctx, member)).To(Succeed())
			Expect(serviceGatewayRuntimeReady(ctx, member, "--enable-service-gateway-export=true")).To(Succeed())
		}
		Expect(serviceGatewayRuntimeReady(ctx, hubCluster, "--enable-service-gateway=true")).To(Succeed())

		class := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "sg-" + testNamespace},
			Spec: gatewayv1.GatewayClassSpec{ControllerName: sg.ControllerName}}
		gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{
			Name: "service-gateway-proof", Namespace: testNamespace,
			Annotations: map[string]string{sg.ConfigAnnotation: string(data)}},
			Spec: gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(class.Name),
				Listeners: []gatewayv1.Listener{{Name: "web", Port: 80, Protocol: gatewayv1.TCPProtocolType}}}}
		route := &gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: testNamespace},
			Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: gatewayv1.ObjectName(gateway.Name), SectionName: ptr.To(gatewayv1.SectionName("web"))}}},
				Rules: []gatewayv1alpha2.TCPRouteRule{{BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group: ptr.To(gatewayv1.Group(fleetnet.GroupVersion.Group)), Kind: ptr.To(gatewayv1.Kind("ServiceImport")),
					Name: "sg-web", Port: ptr.To(gatewayv1.PortNumber(80))}}}}}}}

		// Once any fixtures are created, suite teardown must not implicitly remove
		// an enrolled network on failure. Clear only after full withdrawal proof.
		retainServiceGatewaySetup = true
		DeferCleanup(func(ctx SpecContext) {
			if retainServiceGatewaySetup {
				reportServiceGatewayProof(ctx, api, config, gateway)
			}
		}, NodeTimeout(2*time.Minute))
		for _, member := range memberClusters {
			deployment := serviceGatewayDeployment(testNamespace, member.Name())
			Expect(member.Client().Create(ctx, deployment)).To(Succeed())
			service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "sg-web", Namespace: testNamespace},
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "sg-web"},
					Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}}}}
			Expect(member.Client().Create(ctx, service)).To(Succeed())
			Expect(member.Client().Create(ctx, framework.ServiceExport(testNamespace, "sg-web"))).To(Succeed())
		}
		var expected []sg.Backend
		Eventually(ctx, func() error {
			var err error
			expected, err = serviceGatewayBackends(ctx)
			return err
		}, 10*time.Minute, 5*time.Second).Should(Succeed())
		Expect(hubCluster.Client().Create(ctx, class)).To(Succeed())
		Expect(hubCluster.Client().Create(ctx, gateway)).To(Succeed())
		Expect(hubCluster.Client().Create(ctx, route)).To(Succeed())
		var imported fleetnet.ServiceImport
		Eventually(ctx, func() error {
			if err := hubCluster.Client().Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "sg-web"}, &imported); err != nil {
				return err
			}
			if len(imported.Status.Clusters) != 2 {
				return fmt.Errorf("waiting for both ServiceExports")
			}
			return nil
		}, 10*time.Minute, 5*time.Second).Should(Succeed())
		identity := sg.Identity(string(gateway.UID), string(route.UID)+"/"+string(imported.UID))
		lbID, pipID := config.ApplicationIDs(identity)
		roots = append(roots, lbID, pipID)

		By("observing the installed controller's owned network, application VIP and exact pod locations")
		var vip string
		Eventually(ctx, func() error {
			var current gatewayv1.Gateway
			if err := hubCluster.Client().Get(ctx, client.ObjectKeyFromObject(gateway), &current); err != nil {
				return err
			}
			var err error
			vip, err = serviceGatewayVIP(&current)
			if err != nil {
				return err
			}
			for _, id := range roots {
				resource, err := api.Get(ctx, id)
				if err != nil {
					return err
				}
				if resource.Tags[sg.OwnerTag] != string(gateway.UID) {
					return fmt.Errorf("resource ownership mismatch: %s", id)
				}
				var state string
				if err := json.Unmarshal(resource.Properties["provisioningState"], &state); err != nil {
					return err
				}
				if state != "Succeeded" {
					return fmt.Errorf("resource %s is %s", id, state)
				}
			}
			if err := serviceGatewayLinks(ctx, api, config, config.GatewayID); err != nil {
				return err
			}
			for _, member := range memberClusters {
				if err := serviceGatewayNodesReady(ctx, member); err != nil {
					return err
				}
			}
			if err := serviceGatewayAzureGraph(ctx, api, config, identity, vip); err != nil {
				return err
			}
			locations, err := api.List(ctx, config.GatewayID+"/addressLocations")
			if err != nil {
				return err
			}
			return serviceGatewayMembership(locations, identity, expected)
		}, serviceGatewayProofTimeout, 10*time.Second).Should(Succeed())
		AddReportEntry("Service Gateway observed graph", map[string]any{
			"gateway": config.GatewayID, "gatewayUID": gateway.UID, "lb": lbID, "vip": vip, "backends": expected})

		By("requiring 100 successful requests to reach both member workloads")
		Eventually(ctx, func(ctx context.Context) error {
			return serviceGatewayTraffic(ctx, vip, memberClusterNames)
		}, 10*time.Minute, 10*time.Second).Should(Succeed(),
			"Provision approved client-to-pod targetPort 8080 rules in all applicable NSGs; the test does not weaken network policy")
		AddReportEntry("Service Gateway traffic proof", "100 successful independent TCP requests reached both members")

		By("requiring sustained member health and reflected shared NAT egress before readiness changes")
		natResource, err := api.Get(ctx, config.NATGatewayID)
		Expect(err).NotTo(HaveOccurred())
		var publicIPs []struct {
			ID string `json:"id"`
		}
		Expect(json.Unmarshal(natResource.Properties["publicIpAddresses"], &publicIPs)).To(Succeed())
		Expect(publicIPs).To(HaveLen(1))
		natPIP, err := api.Get(ctx, publicIPs[0].ID)
		Expect(err).NotTo(HaveOccurred())
		var egressIP string
		Expect(json.Unmarshal(natPIP.Properties["ipAddress"], &egressIP)).To(Succeed())
		Expect(egressIP).NotTo(BeEmpty())
		for _, member := range memberClusters {
			probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sg-egress-proof", Namespace: testNamespace},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
					Name: "curl", Image: "curlimages/curl:8.12.1", Command: []string{"/bin/sh", "-ec",
						`for i in 1 2 3 4 5 6; do curl -4 --fail --silent --show-error --connect-timeout 5 --max-time 10 https://api.ipify.org; printf '\n'; sleep 10; done`},
				}}}}
			Expect(member.Client().Create(ctx, probe)).To(Succeed())
		}
		Eventually(ctx, func() error {
			for _, member := range memberClusters {
				if err := serviceGatewayNodesReady(ctx, member); err != nil {
					return err
				}
				var pod corev1.Pod
				if err := member.Client().Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "sg-egress-proof"}, &pod); err != nil {
					return err
				}
				if pod.Status.Phase == corev1.PodFailed {
					return StopTrying("Member " + member.Name() + " NAT egress probe failed")
				}
				if pod.Status.Phase != corev1.PodSucceeded {
					return fmt.Errorf("waiting for sustained NAT egress in %s", member.Name())
				}
				logs, err := member.PodLogs(ctx, testNamespace, "sg-egress-proof", &corev1.PodLogOptions{Container: "curl"})
				if err != nil {
					return err
				}
				if err := serviceGatewayEgressSamples(logs, egressIP); err != nil {
					return fmt.Errorf("member %s: %w", member.Name(), err)
				}
			}
			return nil
		}, 3*time.Minute, 5*time.Second).Should(Succeed())
		AddReportEntry("Service Gateway sustained NAT egress", map[string]any{"publicIP": egressIP, "samplesPerMember": 6, "members": memberClusterNames})

		By("withdrawing one member's ready endpoints while retaining the other and shared egress")
		member := memberClusters[0]
		Expect(serviceGatewaySetReadiness(ctx, member, false)).To(Succeed())
		Eventually(ctx, func() error {
			var remaining []sg.Backend
			for _, backend := range expected {
				if backend.Cluster != member.Name() {
					remaining = append(remaining, backend)
				}
			}
			locations, err := api.List(ctx, config.GatewayID+"/addressLocations")
			if err != nil {
				return err
			}
			if err := serviceGatewayMembership(locations, identity, remaining); err != nil {
				return err
			}
			return serviceGatewayTraffic(ctx, vip, []string{memberClusters[1].Name()})
		}, 10*time.Minute, 10*time.Second).Should(Succeed())
		Expect(serviceGatewayLinks(ctx, api, config, config.GatewayID)).To(Succeed())
		_, err = api.Get(ctx, config.NATGatewayID)
		Expect(err).NotTo(HaveOccurred(), "readiness loss must not delete shared NAT")
		Expect(serviceGatewaySetReadiness(ctx, member, true)).To(Succeed())
		Eventually(ctx, func() error {
			var err error
			expected, err = serviceGatewayBackends(ctx)
			if err != nil {
				return err
			}
			locations, err := api.List(ctx, config.GatewayID+"/addressLocations")
			if err != nil {
				return err
			}
			if err := serviceGatewayMembership(locations, identity, expected); err != nil {
				return err
			}
			return serviceGatewayTraffic(ctx, vip, memberClusterNames)
		}, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("removing the final route and observing subnet restoration before NAT disappears")
		Expect(hubCluster.Client().Delete(ctx, route)).To(Succeed())
		// Observing order cannot prove atomic platform transactions, but a sampled
		// NAT disappearance while a subnet remains linked is an immediate failure.
		Eventually(ctx, func() (bool, error) {
			linkErr := serviceGatewayLinks(ctx, api, config, "")
			_, natErr := api.Get(ctx, config.NATGatewayID)
			if azureerrors.IsNotFound(natErr) && linkErr != nil {
				// Re-read after observing deletion to avoid an old subnet sample
				// falsely reporting a detach/delete ordering violation.
				if currentErr := serviceGatewayLinks(ctx, api, config, ""); currentErr != nil {
					Fail("NAT absent while subnet restoration is not verified: " + currentErr.Error())
				}
				linkErr = nil
			}
			if natErr != nil && !azureerrors.IsNotFound(natErr) {
				return false, natErr
			}
			if linkErr != nil {
				return false, nil
			}
			for _, id := range roots {
				_, err := api.Get(ctx, id)
				if err == nil {
					return false, nil
				}
				if !azureerrors.IsNotFound(err) {
					return false, err
				}
			}
			var current gatewayv1.Gateway
			if err := hubCluster.Client().Get(ctx, client.ObjectKeyFromObject(gateway), &current); err != nil {
				return false, err
			}
			return current.Annotations[sg.JournalAnnotation] == "" && len(current.Status.Addresses) == 0, nil
		}, serviceGatewayProofTimeout, 5*time.Second).Should(BeTrue())
		for _, member := range memberClusters {
			Eventually(ctx, func() error { return serviceGatewayNodesReady(ctx, member) },
				5*time.Minute, 5*time.Second).Should(Succeed(), "original AKS connectivity must recover after detach")
			By("requiring a new ready workload after restoring original AKS egress")
			deployment := serviceGatewayDeployment(testNamespace, member.Name())
			deployment.Name = "sg-post-detach"
			deployment.Spec.Selector.MatchLabels = map[string]string{"app": "sg-post-detach"}
			deployment.Spec.Template.Labels = map[string]string{"app": "sg-post-detach"}
			Expect(member.Client().Create(ctx, deployment)).To(Succeed())
			Eventually(ctx, func() error {
				var current appsv1.Deployment
				if err := member.Client().Get(ctx, client.ObjectKeyFromObject(deployment), &current); err != nil {
					return err
				}
				if current.Status.ObservedGeneration != current.Generation || current.Status.AvailableReplicas != 1 {
					return fmt.Errorf("post-detach workload in member %s is not available", member.Name())
				}
				return nil
			}, 5*time.Minute, 5*time.Second).Should(Succeed())
		}
		Expect(hubCluster.Client().Delete(ctx, gateway)).To(Succeed())
		Eventually(ctx, func() bool {
			return apierrors.IsNotFound(hubCluster.Client().Get(ctx, client.ObjectKeyFromObject(gateway), &gatewayv1.Gateway{}))
		}, time.Minute, time.Second).Should(BeTrue())
		Expect(hubCluster.Client().Delete(ctx, class)).To(Succeed())
		retainServiceGatewaySetup = false
	}, SpecTimeout(2*time.Hour))
})

func serviceGatewayDeployment(namespace, member string) *appsv1.Deployment {
	labels := map[string]string{"app": "sg-web"}
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sg-web", Namespace: namespace},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "web", Image: "mcr.microsoft.com/cbl-mariner/busybox:2.0",
					Env:     []corev1.EnvVar{{Name: "CLUSTER", Value: member}},
					Command: []string{"/bin/sh", "-c", `mkdir -p /www; echo "cluster=$CLUSTER pod=$HOSTNAME" > /www/index.html; exec httpd -f -p 8080 -h /www`},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 2},
				}}}}}}
}

func serviceGatewaySetReadiness(ctx context.Context, member *framework.Cluster, ready bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var deployment appsv1.Deployment
		if err := member.Client().Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "sg-web"}, &deployment); err != nil {
			return err
		}
		path := "/"
		if !ready {
			path = "/not-ready"
		}
		deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path = path
		return member.Client().Update(ctx, &deployment)
	})
}

func serviceGatewayBackends(ctx context.Context) ([]sg.Backend, error) {
	var result []sg.Backend
	for _, member := range memberClusters {
		var slices discoveryv1.EndpointSliceList
		if err := member.Client().List(ctx, &slices, client.InNamespace(testNamespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: "sg-web"}); err != nil {
			return nil, err
		}
		count := 0
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready ||
					ptr.Deref(endpoint.Conditions.Terminating, false) || endpoint.NodeName == nil {
					continue
				}
				var node corev1.Node
				if err := member.Client().Get(ctx, client.ObjectKey{Name: *endpoint.NodeName}, &node); err != nil {
					return nil, err
				}
				var nodeIP string
				for _, address := range node.Status.Addresses {
					if address.Type == corev1.NodeInternalIP {
						if nodeIP != "" {
							return nil, fmt.Errorf("ambiguous node location")
						}
						nodeIP = address.Address
					}
				}
				if nodeIP == "" {
					return nil, fmt.Errorf("missing node InternalIP")
				}
				for _, address := range endpoint.Addresses {
					result = append(result, sg.Backend{Cluster: member.Name(), PodIP: address, NodeIP: nodeIP})
					count++
				}
			}
		}
		if count != 1 {
			return nil, fmt.Errorf("member %s has %d ready pod endpoints, expected one", member.Name(), count)
		}
	}
	return result, nil
}

func serviceGatewayNodesReady(ctx context.Context, member *framework.Cluster) error {
	var nodes corev1.NodeList
	if err := member.Client().List(ctx, &nodes); err != nil {
		return err
	}
	if len(nodes.Items) == 0 {
		return fmt.Errorf("member %s has no nodes", member.Name())
	}
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			ready = ready || condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
		}
		if !ready {
			return fmt.Errorf("node %s/%s is not Ready", member.Name(), node.Name)
		}
		var lease coordinationv1.Lease
		if err := member.Client().Get(ctx, client.ObjectKey{Namespace: corev1.NamespaceNodeLease, Name: node.Name}, &lease); err != nil {
			return err
		}
		if lease.Spec.RenewTime == nil || time.Since(lease.Spec.RenewTime.Time) > time.Minute {
			return fmt.Errorf("node %s/%s has no fresh heartbeat lease", member.Name(), node.Name)
		}
		if time.Since(node.CreationTimestamp.Time) < 30*time.Second {
			return fmt.Errorf("node status has not settled")
		}
	}
	return nil
}

func serviceGatewayRuntimeReady(ctx context.Context, cluster *framework.Cluster, flag string) error {
	var deployments appsv1.DeploymentList
	// All-namespaces discovery allows a configured Fleet namespace without
	// guessing chart release names. Require exactly one ready, current writer.
	if err := cluster.Client().List(ctx, &deployments); err != nil {
		return err
	}
	count := 0
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			enabled := false
			for _, arg := range container.Args {
				enabled = enabled || arg == flag
			}
			if !enabled {
				continue
			}
			if deployment.Status.ObservedGeneration != deployment.Generation ||
				deployment.Status.AvailableReplicas < 1 || deployment.Status.UpdatedReplicas != ptr.Deref(deployment.Spec.Replicas, 1) {
				return fmt.Errorf("controller deployment %s/%s is not current and available", deployment.Namespace, deployment.Name)
			}
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("cluster %s requires exactly one current controller deployment with %s, found %d", cluster.Name(), flag, count)
	}
	return nil
}

func serviceGatewayAzureGraph(ctx context.Context, api sg.API, config sg.Config, identity, vip string) error {
	lbID, pipID := config.ApplicationIDs(identity)
	pip, err := api.Get(ctx, pipID)
	if err != nil {
		return err
	}
	var address string
	if err := json.Unmarshal(pip.Properties["ipAddress"], &address); err != nil {
		return err
	}
	if address != vip {
		return fmt.Errorf("Gateway VIP does not match owned public IP")
	}
	lb, err := api.Get(ctx, lbID)
	if err != nil {
		return err
	}
	if lb.SKU["name"] != "Service" {
		return fmt.Errorf("expected Service-SKU LB")
	}
	var rules []struct {
		Properties struct {
			FrontendPort       int32 `json:"frontendPort"`
			BackendPort        int32 `json:"backendPort"`
			BackendAddressPool struct {
				ID string `json:"id"`
			} `json:"backendAddressPool"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(lb.Properties["loadBalancingRules"], &rules); err != nil {
		return err
	}
	if len(rules) != 1 || rules[0].Properties.FrontendPort != 80 || rules[0].Properties.BackendPort != 8080 ||
		!strings.EqualFold(rules[0].Properties.BackendAddressPool.ID, lbID+"/backendAddressPools/pods") {
		return fmt.Errorf("unexpected Service LB rule or backend pool")
	}
	services, err := api.List(ctx, config.GatewayID+"/services")
	if err != nil {
		return err
	}
	inbound, outbound := false, false
	for _, service := range services {
		if service.Name == identity {
			var kind string
			if err := json.Unmarshal(service.Properties["serviceType"], &kind); err != nil {
				return err
			}
			var pools []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(service.Properties["loadBalancerBackendPools"], &pools); err != nil {
				return err
			}
			inbound = kind == "Inbound" && len(pools) == 1 && strings.EqualFold(pools[0].ID, lbID+"/backendAddressPools/pods")
		}
		if service.Name == "fleet-default-egress" {
			var isDefault bool
			if err := json.Unmarshal(service.Properties["isDefault"], &isDefault); err != nil {
				return err
			}
			var natID string
			if err := json.Unmarshal(service.Properties["publicNatGatewayId"], &natID); err != nil {
				return err
			}
			outbound = isDefault && strings.EqualFold(natID, config.NATGatewayID)
		}
	}
	if !inbound || !outbound {
		return fmt.Errorf("gateway lacks matching inbound/default outbound registrations")
	}
	nat, err := api.Get(ctx, config.NATGatewayID)
	if err != nil {
		return err
	}
	var gateway struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(nat.Properties["serviceGateway"], &gateway); err != nil {
		return err
	}
	if nat.SKU["name"] != "StandardV2" || !strings.EqualFold(gateway.ID, config.GatewayID) {
		return fmt.Errorf("unexpected NAT SKU or gateway association")
	}
	var subnets []json.RawMessage
	if raw := nat.Properties["subnets"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &subnets); err != nil {
			return err
		}
	}
	if len(subnets) != 0 {
		return fmt.Errorf("NAT must not be directly attached to node subnets")
	}
	return nil
}

func serviceGatewayLinks(ctx context.Context, api sg.API, config sg.Config, expected string) error {
	for _, id := range config.SubnetIDs {
		resource, err := api.Get(ctx, id)
		if err != nil {
			return err
		}
		var link struct {
			ID string `json:"id"`
		}
		if raw := resource.Properties["serviceGateway"]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &link); err != nil {
				return err
			}
		}
		var state string
		if err := json.Unmarshal(resource.Properties["provisioningState"], &state); err != nil {
			return err
		}
		if !strings.EqualFold(link.ID, expected) || state != "Succeeded" {
			return fmt.Errorf("subnet %s has gateway %q state %q", id, link.ID, state)
		}
	}
	return nil
}

func serviceGatewayTraffic(ctx context.Context, vip string, members []string) error {
	// Each request must use a new TCP flow; L4 balancing does not redistribute
	// requests multiplexed over one HTTP keep-alive connection.
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var replies []string
	for i := 0; i < 100; i++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+vip+"/", nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		closeErr := response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if response.StatusCode != http.StatusOK || len(body) > 4096 {
			return fmt.Errorf("unexpected HTTP response status=%d size=%d", response.StatusCode, len(body))
		}
		replies = append(replies, strings.TrimSpace(string(body)))
	}
	if err := serviceGatewayReplies(replies, members); err != nil {
		return err
	}
	return nil
}

func reportServiceGatewayProof(ctx context.Context, api sg.API, config sg.Config, gateway *gatewayv1.Gateway) {
	snapshot := map[string]any{"namespace": testNamespace, "gateway": client.ObjectKeyFromObject(gateway),
		"config": config, "retained": true, "cleanupProven": false}
	var current gatewayv1.Gateway
	if err := hubCluster.Client().Get(ctx, client.ObjectKeyFromObject(gateway), &current); err != nil {
		snapshot["gatewayReadError"] = err.Error()
	} else {
		snapshot["gatewayStatus"] = current.Status
	}
	for _, id := range append([]string{config.GatewayID, config.NATGatewayID}, config.SubnetIDs...) {
		resource, err := api.Get(ctx, id)
		if err != nil {
			snapshot[id] = err.Error()
		} else {
			snapshot[id] = resource
		}
	}
	AddReportEntry("Service Gateway retained failure diagnostics", snapshot)
}
