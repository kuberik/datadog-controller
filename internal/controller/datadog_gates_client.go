package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	datadogAPIKeyEnvVar      = "DATADOG_API_KEY"
	datadogAltAPIKeyEnvVar   = "DD_API_KEY"
	datadogAppKeyEnvVar      = "DATADOG_APP_KEY"
	datadogAltAppKeyEnvVar   = "DD_APP_KEY"
	datadogSiteEnvVar        = "DATADOG_SITE"
	datadogAltSiteEnvVar     = "DD_SITE"
	defaultDatadogSite       = "datadoghq.com"
	defaultDatadogHTTPTimout = 10 * time.Second
)

var errDatadogDeploymentGatesNotConfigured = errors.New("datadog deployment gates client is not configured")

type deploymentGateClient interface {
	StartEvaluation(ctx context.Context, input deploymentGateInput) (string, error)
	GetEvaluation(ctx context.Context, evaluationID string) (deploymentGateEvaluation, error)
}

type deploymentGateInput struct {
	Service       string
	Env           string
	Identifier    string
	Version       string
	APMPrimaryTag string
}

type deploymentGateEvaluation struct {
	ID     string
	URL    string
	Status string
	Rules  []deploymentGateRuleEvaluation
}

type deploymentGateRuleEvaluation struct {
	Name   string
	Status string
	Reason string
	DryRun bool
}

type datadogDeploymentGatesClient struct {
	baseURL    string
	apiKey     string
	appKey     string
	httpClient *http.Client
}

func newDatadogDeploymentGatesClientFromEnv() (deploymentGateClient, error) {
	apiKey := firstNonEmpty(os.Getenv(datadogAPIKeyEnvVar), os.Getenv(datadogAltAPIKeyEnvVar))
	if apiKey == "" {
		return nil, fmt.Errorf("%w: missing %s or %s", errDatadogDeploymentGatesNotConfigured, datadogAPIKeyEnvVar, datadogAltAPIKeyEnvVar)
	}

	appKey := firstNonEmpty(os.Getenv(datadogAppKeyEnvVar), os.Getenv(datadogAltAppKeyEnvVar))
	if appKey == "" {
		return nil, fmt.Errorf("%w: missing %s or %s", errDatadogDeploymentGatesNotConfigured, datadogAppKeyEnvVar, datadogAltAppKeyEnvVar)
	}

	site := firstNonEmpty(os.Getenv(datadogSiteEnvVar), os.Getenv(datadogAltSiteEnvVar), defaultDatadogSite)
	return newDatadogDeploymentGatesClient(resolveDatadogAPIBaseURL(site), apiKey, appKey, nil), nil
}

func newDatadogDeploymentGatesClient(baseURL string, apiKey string, appKey string, httpClient *http.Client) deploymentGateClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultDatadogHTTPTimout}
	}

	return &datadogDeploymentGatesClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		appKey:     appKey,
		httpClient: httpClient,
	}
}

func (c *datadogDeploymentGatesClient) StartEvaluation(ctx context.Context, input deploymentGateInput) (string, error) {
	payload := map[string]any{
		"data": map[string]any{
			"type": "deployment_gates_evaluation_request",
			"attributes": map[string]any{
				"service":    input.Service,
				"env":        input.Env,
				"identifier": input.Identifier,
			},
		},
	}

	attributes := payload["data"].(map[string]any)["attributes"].(map[string]any)
	if input.Version != "" {
		attributes["version"] = input.Version
	}
	if input.APMPrimaryTag != "" {
		attributes["apm_primary_tag"] = input.APMPrimaryTag
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal deployment gate evaluation request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/unstable/deployments/gates/evaluation", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create deployment gate evaluation request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("DD-API-KEY", c.apiKey)
	req.Header.Set("DD-APPLICATION-KEY", c.appKey)

	var response struct {
		Data struct {
			Attributes struct {
				EvaluationID string `json:"evaluation_id"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := c.do(req, &response); err != nil {
		return "", err
	}

	if response.Data.Attributes.EvaluationID == "" {
		return "", errors.New("deployment gate evaluation response did not include an evaluation_id")
	}

	return response.Data.Attributes.EvaluationID, nil
}

func (c *datadogDeploymentGatesClient) GetEvaluation(ctx context.Context, evaluationID string) (deploymentGateEvaluation, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/api/unstable/deployments/gates/evaluation/"+url.PathEscape(evaluationID),
		nil,
	)
	if err != nil {
		return deploymentGateEvaluation{}, fmt.Errorf("create deployment gate status request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("DD-API-KEY", c.apiKey)
	req.Header.Set("DD-APPLICATION-KEY", c.appKey)

	var response struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				EvaluationURL string                         `json:"evaluation_url"`
				GateStatus    string                         `json:"gate_status"`
				Rules         []deploymentGateRuleEvaluation `json:"rules"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := c.do(req, &response); err != nil {
		return deploymentGateEvaluation{}, err
	}

	return deploymentGateEvaluation{
		ID:     response.Data.ID,
		URL:    response.Data.Attributes.EvaluationURL,
		Status: response.Data.Attributes.GateStatus,
		Rules:  response.Data.Attributes.Rules,
	}, nil
}

func (c *datadogDeploymentGatesClient) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call datadog deployment gates API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read datadog deployment gates API response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("datadog deployment gates API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode datadog deployment gates API response: %w", err)
	}

	return nil
}

func resolveDatadogAPIBaseURL(site string) string {
	trimmed := strings.TrimSpace(site)
	if trimmed == "" {
		trimmed = defaultDatadogSite
	}

	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
		return strings.TrimRight(trimmed, "/")
	}

	return "https://api." + strings.TrimPrefix(trimmed, "api.")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
