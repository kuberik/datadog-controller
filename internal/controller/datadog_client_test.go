/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
)

// fakeDatadogServer serves canned Datadog API responses and records requests,
// so the real API client (request building, auth, decoding) is exercised.
func fakeDatadogServer(t *testing.T) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var requests []*http.Request
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/monitor/7", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id": 7, "name": "api errors", "type": "query alert",
			"query": "q", "overall_state": "Alert", "tags": ["service:api"],
			"state": {"groups": {"env:prod,service:api": {"name": "env:prod,service:api", "status": "Alert"},
			                     "env:prod,service:db": {"name": "env:prod,service:db", "status": "OK"}}}}`)
	})
	mux.HandleFunc("/api/v1/monitor", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "0" {
			_, _ = fmt.Fprint(w, `[]`)
			return
		}
		_, _ = fmt.Fprint(w, `[{"id": 1, "name": "burn", "type": "slo alert", "query": "q",
			"overall_state": "OK", "tags": ["service:api", "env:prod"]}]`)
	})
	mux.HandleFunc("/api/v2/incidents/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data": {"type": "incidents_search_results", "attributes": {
			"facets": {}, "total": 1, "incidents": [{"data": {"id": "abc", "type": "incidents",
			"attributes": {"title": "API down", "public_id": 42, "state": "active"}}}]}}}`)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(context.Background()))
		if r.Header.Get("DD-API-KEY") != "api-key" || r.Header.Get("DD-APPLICATION-KEY") != "app-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func newTestClient(serverURL string) *datadogAPIClient {
	return newDatadogAPIClient("api-key", "app-key", defaultDatadogSite,
		datadog.ServerConfigurations{{URL: serverURL}})
}

func TestNewDatadogAPIFromEnv(t *testing.T) {
	t.Setenv(datadogAltAPIKeyEnvVar, "api-key")
	t.Setenv(datadogAltAppKeyEnvVar, "app-key")
	t.Setenv(datadogAltSiteEnvVar, "https://api.datadoghq.eu")
	api, err := newDatadogAPIFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if site := api.(*datadogAPIClient).site; site != "datadoghq.eu" {
		t.Fatalf("site = %q, want datadoghq.eu", site)
	}
}

func TestDatadogAPIClientGetMonitor(t *testing.T) {
	server, requests := fakeDatadogServer(t)
	monitor, err := newTestClient(server.URL).GetMonitor(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetMonitor: %v", err)
	}
	if monitor.Name != "api errors" || monitor.OverallState != monitorStatusAlert {
		t.Fatalf("unexpected monitor %+v", monitor)
	}
	if monitor.Groups["env:prod,service:api"] != monitorStatusAlert || monitor.Groups["env:prod,service:db"] != "OK" {
		t.Fatalf("unexpected groups %+v", monitor.Groups)
	}
	if got := (*requests)[0].URL.Query().Get("group_states"); got != "all" {
		t.Fatalf("group_states = %q, want all", got)
	}
}

func TestDatadogAPIClientListMonitorsByTags(t *testing.T) {
	server, requests := fakeDatadogServer(t)
	monitors, err := newTestClient(server.URL).ListMonitorsByTags(context.Background(), []string{"service:api", "env:prod"})
	if err != nil {
		t.Fatalf("ListMonitorsByTags: %v", err)
	}
	if len(monitors) != 1 || monitors[0].ID != 1 || monitors[0].OverallState != "OK" {
		t.Fatalf("unexpected monitors %+v", monitors)
	}
	query := (*requests)[0].URL.Query()
	if query.Get("monitor_tags") != "service:api,env:prod" || query.Get("group_states") != "all" {
		t.Fatalf("unexpected query %s", (*requests)[0].URL.RawQuery)
	}
}

func TestDatadogAPIErrorsIncludeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"errors": ["Invalid query"]}`)
	}))
	t.Cleanup(server.Close)
	_, err := newTestClient(server.URL).SearchOpenIncidents(context.Background(), "services:api")
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "Invalid query") {
		t.Fatalf("error = %v, want status and response body", err)
	}
}

func TestDatadogAPIClientSearchOpenIncidents(t *testing.T) {
	server, requests := fakeDatadogServer(t)
	incidents, err := newTestClient(server.URL).SearchOpenIncidents(context.Background(), "services:api")
	if err != nil {
		t.Fatalf("SearchOpenIncidents: %v", err)
	}
	if len(incidents) != 1 || incidents[0].PublicID != 42 || incidents[0].Title != "API down" {
		t.Fatalf("unexpected incidents %+v", incidents)
	}
	if query := (*requests)[0].URL.Query().Get("query"); query != "state:(active OR stable) AND services:api" {
		t.Fatalf("unexpected incident query %q", query)
	}
}
