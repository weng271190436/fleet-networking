/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package main

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	ctrl "sigs.k8s.io/controller-runtime"

	"go.goms.io/fleet-networking/pkg/controllers/hub/servicegateway"
	provider "go.goms.io/fleet-networking/pkg/providers/azure/servicegateway"
	"go.goms.io/fleet/pkg/utils/cloudconfig/azure"
)

func setupServiceGateway(mgr ctrl.Manager, configPath string) error {
	config, err := azure.NewCloudConfigFromFile(configPath)
	if err != nil {
		return err
	}
	config.SetUserAgent("fleet-hub-service-gateway")
	auth, err := azclient.NewAuthProvider(&config.ARMClientConfig, &config.AzureAuthConfig)
	if err != nil {
		return fmt.Errorf("configure Service Gateway Azure identity: %w", err)
	}
	options, err := azclient.GetDefaultResourceClientOption(&config.ARMClientConfig)
	if err != nil {
		return err
	}
	api, err := provider.NewAPI(auth.GetAzIdentity(), &arm.ClientOptions{ClientOptions: options.ClientOptions})
	if err != nil {
		return err
	}
	return (&servicegateway.Reconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Provider: &provider.Provider{API: api}}).SetupWithManager(mgr)
}
