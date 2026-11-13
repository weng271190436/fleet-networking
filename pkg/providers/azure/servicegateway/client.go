/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
)

type Resource struct {
	ID              string                     `json:"id,omitempty"`
	Name            string                     `json:"name,omitempty"`
	ETag            string                     `json:"etag,omitempty"`
	Location        string                     `json:"location,omitempty"`
	Tags            map[string]string          `json:"tags,omitempty"`
	SKU             map[string]string          `json:"sku,omitempty"`
	Zones           []string                   `json:"zones,omitempty"`
	Properties      map[string]json.RawMessage `json:"properties,omitempty"`
	AddressLocation string                     `json:"addressLocation,omitempty"`
	Addresses       []GatewayAddress           `json:"addresses,omitempty"`
}

type GatewayAddress struct {
	Address  string   `json:"address"`
	Services []string `json:"services"`
}

// API uses the existing Azure credential/retry boundary; implementations must not
// follow untrusted URLs or suppress failed resource operations.
type API interface {
	Get(context.Context, string) (Resource, error)
	List(context.Context, string) ([]Resource, error)
	Put(context.Context, string, Resource, string) error
	Delete(context.Context, string, string) error
	Post(context.Context, string, any) error
}

type ARMAPI struct{ client *arm.Client }

func NewAPI(credential azcore.TokenCredential, options *arm.ClientOptions) (*ARMAPI, error) {
	client, err := arm.NewClient("go.goms.io/fleet-networking/servicegateway", "v0.1.0", credential, options)
	if err != nil {
		return nil, err
	}
	return &ARMAPI{client: client}, nil
}

func (a *ARMAPI) request(ctx context.Context, method, id string, body any, etag string) (*http.Response, error) {
	if !strings.HasPrefix(id, "/subscriptions/") || strings.ContainsAny(id, "?#") {
		return nil, fmt.Errorf("invalid ARM request ID %q", id)
	}
	endpoint := strings.TrimRight(a.client.Endpoint(), "/") + id + "?api-version=2025-01-01"
	return a.requestEndpoint(ctx, method, endpoint, body, etag)
}

func (a *ARMAPI) requestEndpoint(ctx context.Context, method, endpoint string, body any, etag string) (*http.Response, error) {
	req, err := runtime.NewRequest(ctx, method, endpoint)
	if err != nil {
		return nil, err
	}
	if etag == "*" {
		req.Raw().Header.Set("If-None-Match", "*")
	} else if etag != "" {
		req.Raw().Header.Set("If-Match", etag)
	}
	if body != nil {
		if err := runtime.MarshalAsJSON(req, body); err != nil {
			return nil, err
		}
	}
	response, err := a.client.Pipeline().Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, runtime.NewResponseError(response)
	}
	return response, nil
}

func (a *ARMAPI) Get(ctx context.Context, id string) (Resource, error) {
	response, err := a.request(ctx, http.MethodGet, id, nil, "")
	if err != nil {
		return Resource{}, err
	}
	var resource Resource
	err = runtime.UnmarshalAsJSON(response, &resource)
	return resource, err
}

func (a *ARMAPI) List(ctx context.Context, id string) ([]Resource, error) {
	var result []Resource
	endpoint := strings.TrimRight(a.client.Endpoint(), "/") + id + "?api-version=2025-01-01"
	seen := map[string]bool{}
	for {
		if seen[endpoint] {
			return nil, fmt.Errorf("cyclic ARM pagination")
		}
		seen[endpoint] = true
		response, err := a.requestEndpoint(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		var page struct {
			Value    []Resource `json:"value"`
			NextLink string     `json:"nextLink"`
		}
		if err := runtime.UnmarshalAsJSON(response, &page); err != nil {
			return nil, err
		}
		result = append(result, page.Value...)
		if page.NextLink == "" {
			return result, nil
		}
		next, err := url.Parse(page.NextLink)
		base, baseErr := url.Parse(a.client.Endpoint())
		if err != nil || baseErr != nil || next.Host != base.Host || next.Scheme != base.Scheme {
			return nil, fmt.Errorf("untrusted ARM pagination link")
		}
		if next.Query().Get("api-version") != "2025-01-01" {
			return nil, fmt.Errorf("unsupported pagination API version")
		}
		if next.Path != id {
			return nil, fmt.Errorf("pagination changed resource scope")
		}
		endpoint = page.NextLink
	}
}

func (a *ARMAPI) Put(ctx context.Context, id string, body Resource, etag string) error {
	response, err := a.request(ctx, http.MethodPut, id, body, etag)
	if err != nil {
		return err
	}
	return discardResponse(response)
}
func (a *ARMAPI) Delete(ctx context.Context, id, etag string) error {
	response, err := a.request(ctx, http.MethodDelete, id, nil, etag)
	if err != nil {
		return err
	}
	return discardResponse(response)
}
func (a *ARMAPI) Post(ctx context.Context, id string, body any) error {
	response, err := a.request(ctx, http.MethodPost, id, body, "")
	if err != nil {
		return err
	}
	return discardResponse(response)
}

func discardResponse(response *http.Response) error {
	_, err := io.Copy(io.Discard, response.Body)
	return errors.Join(err, response.Body.Close())
}

func properties(value any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	err = json.Unmarshal(data, &result)
	return result, err
}
