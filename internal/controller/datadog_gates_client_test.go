package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDatadogDeploymentGatesClientStartEvaluation(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/unstable/deployments/gates/evaluation" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}

		if got := r.Header.Get("DD-API-KEY"); got != "api-key" {
			t.Fatalf("unexpected DD-API-KEY header: %s", got)
		}

		if got := r.Header.Get("DD-APPLICATION-KEY"); got != "app-key" {
			t.Fatalf("unexpected DD-APPLICATION-KEY header: %s", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		data := payload["data"].(map[string]any)
		attributes := data["attributes"].(map[string]any)
		if got := attributes["service"]; got != "payments" {
			t.Fatalf("unexpected service: %#v", got)
		}
		if got := attributes["env"]; got != "prod" {
			t.Fatalf("unexpected env: %#v", got)
		}
		if got := attributes["identifier"]; got != "canary" {
			t.Fatalf("unexpected identifier: %#v", got)
		}
		if got := attributes["version"]; got != "1.2.3" {
			t.Fatalf("unexpected version: %#v", got)
		}
		if got := attributes["apm_primary_tag"]; got != "team:platform" {
			t.Fatalf("unexpected apm_primary_tag: %#v", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"attributes":{"evaluation_id":"eval-123"}}}`))
	}))
	defer server.Close()

	client := newDatadogDeploymentGatesClient(server.URL, "api-key", "app-key", server.Client())
	evaluationID, err := client.StartEvaluation(context.Background(), deploymentGateInput{
		Service:       "payments",
		Env:           "prod",
		Identifier:    "canary",
		Version:       "1.2.3",
		APMPrimaryTag: "team:platform",
	})
	if err != nil {
		t.Fatalf("StartEvaluation returned error: %v", err)
	}

	if evaluationID != "eval-123" {
		t.Fatalf("unexpected evaluation ID: %s", evaluationID)
	}
}

func TestDatadogDeploymentGatesClientGetEvaluation(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/unstable/deployments/gates/evaluation/eval-123" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"id": "eval-123",
				"attributes": {
					"evaluation_url": "https://app.datadoghq.com/ci/deployment-gates/eval-123",
					"gate_status": "pass",
					"rules": [
						{
							"name": "latency",
							"status": "pass",
							"reason": "within threshold",
							"dry_run": false
						}
					]
				}
			}
		}`))
	}))
	defer server.Close()

	client := newDatadogDeploymentGatesClient(server.URL, "api-key", "app-key", server.Client())
	evaluation, err := client.GetEvaluation(context.Background(), "eval-123")
	if err != nil {
		t.Fatalf("GetEvaluation returned error: %v", err)
	}

	if evaluation.Status != "pass" {
		t.Fatalf("unexpected gate status: %s", evaluation.Status)
	}

	if evaluation.URL != "https://app.datadoghq.com/ci/deployment-gates/eval-123" {
		t.Fatalf("unexpected evaluation URL: %s", evaluation.URL)
	}

	if len(evaluation.Rules) != 1 || evaluation.Rules[0].Name != "latency" {
		t.Fatalf("unexpected rules: %#v", evaluation.Rules)
	}
}

func TestResolveDatadogAPIBaseURL(t *testing.T) {
	t.Helper()

	testCases := map[string]string{
		"":                          "https://api.datadoghq.com",
		"datadoghq.eu":              "https://api.datadoghq.eu",
		"api.us3.datadoghq.com":     "https://api.us3.datadoghq.com",
		"https://custom.example.io": "https://custom.example.io",
	}

	for input, expected := range testCases {
		if got := resolveDatadogAPIBaseURL(input); got != expected {
			t.Fatalf("resolveDatadogAPIBaseURL(%q) = %q, want %q", input, got, expected)
		}
	}
}
