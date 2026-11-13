/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type clientTestCredential struct{}

func (clientTestCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type clientTestTransport func(*http.Request) (*http.Response, error)

func (f clientTestTransport) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestNetworkCreateRechecksAbsenceWithoutUnsupportedHeader(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "appeared"}[exists], func(t *testing.T) {
			puts := 0
			api, err := NewAPI(clientTestCredential{}, &arm.ClientOptions{ClientOptions: policy.ClientOptions{
				Transport: clientTestTransport(func(req *http.Request) (*http.Response, error) {
					code, body := http.StatusOK, `{"id":"existing"}`
					if req.Method == http.MethodGet && !exists {
						code, body = http.StatusNotFound, `{"error":{"code":"NotFound"}}`
					}
					if req.Method == http.MethodPut {
						puts++
						if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Match") != "" {
							t.Fatal("unsupported create condition sent")
						}
						code, body = http.StatusCreated, `{}`
					}
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			err = api.Put(context.Background(), "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/test/providers/Microsoft.Network/publicIPAddresses/test", Resource{}, "*")
			if exists && (err == nil || puts != 0) {
				t.Fatal("existing resource overwritten")
			}
			if !exists && (err != nil || puts != 1) {
				t.Fatalf("create failed: %v puts=%d", err, puts)
			}
		})
	}
}
