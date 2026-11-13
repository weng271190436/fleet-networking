//go:build e2e

/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestServiceGatewayTrafficUsesIndependentFlows(t *testing.T) {
	var connections, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		member := "member-1"
		if requests.Add(1)%2 == 0 {
			member = "member-2"
		}
		fmt.Fprintf(w, "cluster=%s pod=web\n", member)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	if err := serviceGatewayTraffic(context.Background(), strings.TrimPrefix(server.URL, "http://"), []string{"member-1", "member-2"}); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 100 {
		t.Fatalf("expected 100 independent TCP flows, got %d", connections.Load())
	}
}
