#!/bin/bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/e2e-config.sh"
if [[ "$ENABLE_SERVICE_GATEWAY_PROOF" != true ]]; then
  echo "error: dedicated Service Gateway setup requires ENABLE_SERVICE_GATEWAY_PROOF=true" >&2
  exit 1
fi
if [[ "${SERVICE_GATEWAY_CUTOVER_APPROVED:-false}" != true ]]; then
  echo "error: set SERVICE_GATEWAY_CUTOVER_APPROVED=true only after approving billable setup, public authenticated AKS APIs, and existing-node subnet egress cutover" >&2
  exit 1
fi
if [[ ! "${SERVICE_GATEWAY_USER_OBJECT_ID:-}" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]]; then
  echo "error: SERVICE_GATEWAY_USER_OBJECT_ID must be your Entra user Object ID" >&2
  exit 1
fi
for tool in az kubectl kubelogin helm jq curl python3 ssh-keygen docker go timeout; do
  command -v "$tool" >/dev/null || { echo "error: missing $tool" >&2; exit 1; }
done
docker info >/dev/null
go version
go list -m -f '{{.GoVersion}}' >/dev/null
location="${LOCATION:-eastus2euap}"
version="${KUBERNETES_VERSION:-1.36.3}"
client_ip=$(curl -4 --fail --silent --show-error --max-time 10 https://api.ipify.org)
jq -en --arg ip "$client_ip" '$ip|split(".")|length==4 and all(.[];test("^[0-9]{1,3}$") and tonumber<=255)' >/dev/null
features=(Microsoft.Network/AllowServiceGateways Microsoft.Network/AllowServiceSkuLoadBalancer)
if [[ "$location" == *euap ]]; then features+=(Microsoft.Resources/EUAPParticipation); fi
for feature in "${features[@]}"; do
  state=$(az feature show --subscription "$AZURE_SUBSCRIPTION_ID" --namespace "${feature%%/*}" \
    --name "${feature#*/}" --query properties.state -o tsv)
  [[ "$state" == Registered ]] || { echo "error: $feature is $state; obtain preview enrollment" >&2; exit 1; }
done
for provider in Microsoft.ContainerService Microsoft.Network Microsoft.ManagedIdentity Microsoft.Authorization Microsoft.ContainerRegistry; do
  state=$(az provider show --subscription "$AZURE_SUBSCRIPTION_ID" --namespace "$provider" --query registrationState -o tsv)
  [[ "$state" == Registered ]] || { echo "error: provider $provider is $state" >&2; exit 1; }
done
if [[ "$(az group exists --subscription "$AZURE_SUBSCRIPTION_ID" -n "$AZURE_RESOURCE_GROUP")" == true ]]; then
  echo "error: use a fresh dedicated resource group; inspect any previous failed setup before choosing another" >&2
  exit 1
fi
if [[ -e "$SERVICE_GATEWAY_STATE_DIR" ]]; then
  echo "error: local state already exists; inspect $SERVICE_GATEWAY_STATE_DIR before retrying" >&2
  exit 1
fi
umask 077
mkdir -p "$SERVICE_GATEWAY_STATE_DIR"
trap 'status=$?; echo "Service Gateway setup failed (status $status). All Azure resources and local state are retained: $SERVICE_GATEWAY_STATE_DIR" >&2' ERR
trap 'echo "Interrupted; all Service Gateway setup resources are retained" >&2; exit 130' INT
trap 'echo "Terminated; all Service Gateway setup resources are retained" >&2; exit 143' TERM
base="/subscriptions/$AZURE_SUBSCRIPTION_ID/resourceGroups/$AZURE_RESOURCE_GROUP"
arm=https://management.azure.com
role() {
  local principal="$1" kind="$2" definition="$3" scope="$4" name body
  name=$(python3 -c 'import sys,uuid;print(uuid.uuid5(uuid.NAMESPACE_URL,"|".join(sys.argv[1:]).lower()))' \
    "$principal" "$definition" "$scope")
  body=$(jq -n --arg p "$principal" --arg k "$kind" \
    --arg r "/subscriptions/$AZURE_SUBSCRIPTION_ID/providers/Microsoft.Authorization/roleDefinitions/$definition" \
    '{properties:{principalId:$p,principalType:$k,roleDefinitionId:$r}}')
  az rest --subscription "$AZURE_SUBSCRIPTION_ID" --resource "$arm/" --method put \
    --url "$arm$scope/providers/Microsoft.Authorization/roleAssignments/$name?api-version=2022-04-01" \
    --body "$body" -o none
}
az group create --subscription "$AZURE_SUBSCRIPTION_ID" -n "$AZURE_RESOURCE_GROUP" -l "$location" \
  --tags purpose=fleet-service-gateway-e2e retain=true -o none
az network vnet create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
  -n members -l "$location" --address-prefixes 172.24.0.0/16 \
  --subnet-name member-1 --subnet-prefixes 172.24.0.0/22 -o none
az network vnet subnet create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
  --vnet-name members -n member-2 --address-prefixes 172.24.4.0/22 -o none
ssh-keygen -q -t rsa -b 3072 -N '' -f "$SERVICE_GATEWAY_STATE_DIR/id_rsa"
key=$(cat "$SERVICE_GATEWAY_STATE_DIR/id_rsa.pub")
declare -A clients principals node_groups
for cluster in hub member-1 member-2; do
  echo "Creating $cluster (ordinary Standard LB/Azure CNI, Entra/Azure RBAC enabled)"
  identity=$(az identity create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
    -n "$cluster-control-plane" -l "$location" -o json)
  identity_id=$(jq -er .id <<<"$identity")
  principal=$(jq -er .principalId <<<"$identity")
  role "$principal" ServicePrincipal b24988ac-6180-42a0-ab88-20f7382dd24c "$base"
  subnet=""; cidr=10.102.0.0/16; dns=10.102.0.10
  if [[ "$cluster" == member-1 ]]; then
    subnet="$base/providers/Microsoft.Network/virtualNetworks/members/subnets/member-1"
    cidr=10.100.0.0/16; dns=10.100.0.10
  elif [[ "$cluster" == member-2 ]]; then
    subnet="$base/providers/Microsoft.Network/virtualNetworks/members/subnets/member-2"
    cidr=10.101.0.0/16; dns=10.101.0.10
  fi
  jq -n --arg name "$cluster" --arg location "$location" --arg version "$version" \
    --arg identity "$identity_id" --arg subnet "$subnet" --arg key "$key" --arg cidr "$cidr" --arg dns "$dns" '{
    "$schema":"https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#",
    contentVersion:"1.0.0.0",resources:[{
      type:"Microsoft.ContainerService/managedClusters",apiVersion:"2026-05-02-preview",
      name:$name,location:$location,identity:{type:"UserAssigned",userAssignedIdentities:{($identity):{}}},
      sku:{name:"Base",tier:"Free"},properties:{
        dnsPrefix:$name,kubernetesVersion:$version,enableRBAC:true,disableLocalAccounts:true,
        aadProfile:{managed:true,enableAzureRBAC:true},apiServerAccessProfile:{authorizedIPRanges:[]},
        servicePrincipalProfile:{clientId:"msi"},
        linuxProfile:{adminUsername:"azureuser",ssh:{publicKeys:[{keyData:$key}]}},
        agentPoolProfiles:[({name:"system",mode:"System",type:"VirtualMachineScaleSets",
          count:(if $name=="hub" then 1 else 2 end),vmSize:"Standard_D4s_v4",osType:"Linux",
          osSKU:"Ubuntu",osDiskType:"Managed",osDiskSizeGB:128,maxPods:30}+
          (if $subnet=="" then {} else {vnetSubnetID:$subnet} end))],
        networkProfile:{networkPlugin:"azure",networkDataplane:"azure",loadBalancerSku:"standard",
          loadBalancerProfile:{backendPoolType:"nodeIPConfiguration"},outboundType:"loadBalancer",
          serviceCidr:$cidr,dnsServiceIP:$dns,ipFamilies:["IPv4"]}
      }}]}' > "$SERVICE_GATEWAY_STATE_DIR/$cluster.json"
  az deployment group validate --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
    --template-file "$SERVICE_GATEWAY_STATE_DIR/$cluster.json" -o none
  timeout 45m az deployment group create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
    -n "$cluster" --template-file "$SERVICE_GATEWAY_STATE_DIR/$cluster.json" -o none
  cluster_json=$(az aks show --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" -n "$cluster" -o json)
  [[ "$(jq -er .provisioningState <<<"$cluster_json")" == Succeeded ]]
  cluster_id=$(jq -er .id <<<"$cluster_json")
  role "$SERVICE_GATEWAY_USER_OBJECT_ID" User b1ff04bb-8a4e-4dc4-8eb5-8693973ce19b "$cluster_id"
  az aks get-credentials --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" -n "$cluster" \
    --file "$KUBECONFIG" --context "$cluster-admin"
  kubelogin convert-kubeconfig --login azurecli --context "$cluster-admin"
  timeout 10m bash -c 'until kubectl --context "$1" get nodes; do sleep 15; done' _ "$cluster-admin"
  kubectl --context "$cluster-admin" wait --for=condition=Ready nodes --all --timeout=10m
  clients[$cluster]=$(jq -er '.identityProfile.kubeletidentity.clientId' <<<"$cluster_json")
  principals[$cluster]=$(jq -er '.identityProfile.kubeletidentity.objectId' <<<"$cluster_json")
  node_groups[$cluster]=$(jq -er .nodeResourceGroup <<<"$cluster_json")
done

# Private ACR pulls use existing kubelet identities, not anonymous registry access.
acr_name=$(python3 -c 'import hashlib,sys; print("fleetsg"+hashlib.sha256(sys.argv[1].encode()).hexdigest()[:20])' "$base")
az acr create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" \
  -n "$acr_name" -l "$location" --sku Standard -o none
registry=$(az acr show --subscription "$AZURE_SUBSCRIPTION_ID" -n "$acr_name" --query loginServer -o tsv)
for cluster in hub member-1 member-2; do
  role "${principals[$cluster]}" ServicePrincipal 7f951dda-4ed3-4680-a7ca-43fe172d538d "$base/providers/Microsoft.ContainerRegistry/registries/$acr_name"
done
az acr login --subscription "$AZURE_SUBSCRIPTION_ID" -n "$acr_name"
tag="sg-$(date -u +%Y%m%d%H%M%S)"
for component in hub-net-controller-manager member-net-controller-manager hub-gateway-controller-manager; do
  docker build --platform linux/amd64 --build-arg GOARCH=amd64 --build-arg GOOS=linux \
    -f "docker/$component.Dockerfile" -t "$registry/$component:$tag" .
  az acr login --subscription "$AZURE_SUBSCRIPTION_ID" -n "$acr_name"
  docker push "$registry/$component:$tag"
done

# Install existing Fleet transports; no mock hub objects or direct provider provisioning.
fleet_dir=$(go list -m -f '{{.Dir}}' go.goms.io/fleet)
gateway_dir=$(go list -m -f '{{.Dir}}' sigs.k8s.io/gateway-api)
kubectl --context hub-admin apply --server-side -f "$fleet_dir/config/crd/bases/cluster.kubernetes-fleet.io_internalmemberclusters.yaml"
for crd in gatewayclasses gateways tcproutes; do
  kubectl --context hub-admin apply --server-side \
    -f "$gateway_dir/config/crd/experimental/gateway.networking.k8s.io_$crd.yaml"
  kubectl --context hub-admin wait --for=condition=Established --timeout=60s "crd/$crd.gateway.networking.k8s.io"
done
for cluster in hub member-1 member-2; do
  for crd in config/crd/bases/*.yaml; do
    kubectl --context "$cluster-admin" apply --server-side -f "$crd"
  done
  kubectl --context "$cluster-admin" create namespace fleet-system --dry-run=client -o yaml |
    kubectl --context "$cluster-admin" apply -f -
done
helm install e2e-hub-resources ./examples/getting-started/charts/hub --kube-context hub-admin \
  --set 'memberClusterConfigs[0].memberID=member-1' --set "memberClusterConfigs[0].principalID=${principals[member-1]}" \
  --set 'memberClusterConfigs[1].memberID=member-2' --set "memberClusterConfigs[1].principalID=${principals[member-2]}"
# Azure RBAC-enabled API servers need an explicit data-plane grant in addition
# to the existing namespace-scoped Kubernetes RBAC bindings for member agents.
for member in member-1 member-2; do
  role "${principals[$member]}" ServicePrincipal a7ffa36f-339b-4b5c-8bdf-e2c188b2c0eb \
    "$base/providers/Microsoft.ContainerService/managedClusters/hub/namespaces/fleet-member-$member"
done
helm install hub-net-controller-manager ./charts/hub-net-controller-manager --kube-context hub-admin \
  --namespace fleet-system --set "image.repository=$registry/hub-net-controller-manager" \
  --set "image.tag=$tag" --set enableTrafficManagerFeature=false --wait --timeout 15m
hub_url=$(kubectl --context hub-admin config view --minify -o jsonpath='{.clusters[0].cluster.server}')
for member in member-1 member-2; do
  helm install e2e-member-resources ./examples/getting-started/charts/members \
    --kube-context "$member-admin" --set "memberID=$member"
  helm install member-net-controller-manager ./charts/member-net-controller-manager \
    --kube-context "$member-admin" --namespace fleet-system \
    --set "image.repository=$registry/member-net-controller-manager" --set "image.tag=$tag" \
    --set enableTrafficManagerFeature=false --set enableServiceGatewayExport=true \
    --set "config.hubURL=$hub_url" --set config.provider=azure \
    --set "config.memberClusterName=$member" --set "azure.clientid=${clients[$member]}" \
    --wait --timeout 15m
done
role "${principals[hub]}" ServicePrincipal b24988ac-6180-42a0-ab88-20f7382dd24c "$base"
tenant=$(az account show --subscription "$AZURE_SUBSCRIPTION_ID" --query tenantId -o tsv)
cloud=$(jq -n --arg sub "$AZURE_SUBSCRIPTION_ID" --arg tenant "$tenant" --arg client "${clients[hub]}" \
  --arg group "$AZURE_RESOURCE_GROUP" --arg location "$location" '{
    cloud:"AzurePublicCloud",subscriptionId:$sub,tenantId:$tenant,useManagedIdentityExtension:true,
    userAssignedIdentityID:$client,resourceGroup:$group,location:$location}')
kubectl --context hub-admin -n fleet-system create secret generic service-gateway-cloud-config \
  --from-literal="azure.json=$cloud"
helm install hub-gateway-controller-manager ./charts/hub-gateway-controller-manager --kube-context hub-admin \
  --namespace fleet-system --set "image.repository=$registry/hub-gateway-controller-manager" \
  --set "image.tag=$tag" --set afd.enabled=false --set serviceGateway.enabled=true \
  --set gatewayClass.create=false --set afd.cloudConfigSecret.name=service-gateway-cloud-config \
  --wait --timeout 15m

# Narrow ingress policy is provisioned before new test pod addresses are known.
for member in member-1 member-2; do
  node_group="${node_groups[$member]}"
  nsg_ids=$(az vmss list --subscription "$AZURE_SUBSCRIPTION_ID" -g "$node_group" -o json |
    jq -er '[.[].virtualMachineProfile.networkProfile.networkInterfaceConfigurations[]?.networkSecurityGroup.id // empty]|unique|.[]')
  subnet_id="$base/providers/Microsoft.Network/virtualNetworks/members/subnets/$member"
  subnet_json=$(az rest --subscription "$AZURE_SUBSCRIPTION_ID" --resource "$arm/" --method get \
    --url "$arm$subnet_id?api-version=2025-01-01" -o json)
  subnet_nsg=$(jq -er '.properties.networkSecurityGroup.id' <<<"$subnet_json")
  prefix=$(jq -er '.properties.addressPrefix' <<<"$subnet_json")
  while IFS= read -r nsg; do
    nsg_group=$(cut -d/ -f5 <<<"$nsg")
    nsg_name=${nsg##*/}
    priority=$(az network nsg show --subscription "$AZURE_SUBSCRIPTION_ID" -g "$nsg_group" -n "$nsg_name" -o json |
      jq -er '[.securityRules[].priority] as $used|[range(200;4097)|. as $n|select($used|index($n)|not)][0]')
    az network nsg rule create --subscription "$AZURE_SUBSCRIPTION_ID" -g "$nsg_group" --nsg-name "$nsg_name" \
      -n fleet-sg-e2e-client --priority "$priority" --access Allow --protocol Tcp \
      --source-address-prefixes "$client_ip/32" --destination-address-prefixes "$prefix" \
      --destination-port-ranges 8080 -o none
  done < <(printf '%s\n%s\n' "$nsg_ids" "$subnet_nsg" | sort -u)
done
jq -n --arg base "$base/providers/Microsoft.Network" --arg location "$location" '{
  location:$location,gatewayID:($base+"/serviceGateways/e2e"),vnetID:($base+"/virtualNetworks/members"),
  subnetIDs:[($base+"/virtualNetworks/members/subnets/member-1"),($base+"/virtualNetworks/members/subnets/member-2")],
  natGatewayID:($base+"/natGateways/e2e"),egressPublicIPID:($base+"/publicIPAddresses/e2e-egress"),
  approveSubnetEgressCutover:true
}' > "$SERVICE_GATEWAY_PROOF_CONFIG"
printf 'subscription=%s\nresourceGroup=%s\nregistry=%s\nimageTag=%s\nclient=%s\n' \
  "$AZURE_SUBSCRIPTION_ID" "$AZURE_RESOURCE_GROUP" "$registry" "$tag" "$client_ip" > "$SERVICE_GATEWAY_STATE_DIR/setup-complete"
echo "Setup complete. Run make e2e-tests with the same environment variables."
echo "All clusters and resources are retained. Private state: $SERVICE_GATEWAY_STATE_DIR"
