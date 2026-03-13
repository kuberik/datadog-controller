package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
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
	apiClient       *datadog.APIClient
	apiKeys         map[string]datadog.APIKey
	serverVariables map[string]string
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
	return newDatadogDeploymentGatesClient(site, apiKey, appKey, nil), nil
}

func newDatadogDeploymentGatesClient(baseURL string, apiKey string, appKey string, httpClient *http.Client) deploymentGateClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultDatadogHTTPTimout}
	}

	cfg := datadog.NewConfiguration()
	cfg.HTTPClient = httpClient

	client := &datadogDeploymentGatesClient{
		apiClient: datadog.NewAPIClient(cfg),
		apiKeys: map[string]datadog.APIKey{
			"apiKeyAuth": {Key: apiKey},
			"appKeyAuth": {Key: appKey},
		},
	}

	trimmed := strings.TrimSpace(baseURL)
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
		cfg.Servers = datadog.ServerConfigurations{
			{
				URL:         strings.TrimRight(trimmed, "/"),
				Description: "custom Datadog deployment gates server",
			},
		}
		return client
	}

	client.serverVariables = map[string]string{
		"site": normalizeDatadogSite(trimmed),
	}

	return client
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

	var response struct {
		Data struct {
			Attributes struct {
				EvaluationID string `json:"evaluation_id"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := c.do(ctx, http.MethodPost, "/api/unstable/deployments/gates/evaluation", payload, &response); err != nil {
		return "", err
	}

	if response.Data.Attributes.EvaluationID == "" {
		return "", errors.New("deployment gate evaluation response did not include an evaluation_id")
	}

	return response.Data.Attributes.EvaluationID, nil
}

func (c *datadogDeploymentGatesClient) GetEvaluation(ctx context.Context, evaluationID string) (deploymentGateEvaluation, error) {
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

	if err := c.do(ctx, http.MethodGet, "/api/unstable/deployments/gates/evaluation/"+url.PathEscape(evaluationID), nil, &response); err != nil {
		return deploymentGateEvaluation{}, err
	}

	return deploymentGateEvaluation{
		ID:     response.Data.ID,
		URL:    response.Data.Attributes.EvaluationURL,
		Status: response.Data.Attributes.GateStatus,
		Rules:  response.Data.Attributes.Rules,
	}, nil
}

func (c *datadogDeploymentGatesClient) do(ctx context.Context, method string, path string, body any, out any) error {
	requestContext := c.requestContext(ctx)
	baseURL, err := c.apiClient.Cfg.ServerURLWithContext(requestContext, "deploymentGatesEvaluation")
	if err != nil {
		return fmt.Errorf("resolve datadog deployment gates server URL: %w", err)
	}

	headerParams := map[string]string{
		"Accept": "application/json",
	}
	if body != nil {
		headerParams["Content-Type"] = "application/json"
	}

	datadog.SetAuthKeys(
		requestContext,
		&headerParams,
		[2]string{"apiKeyAuth", "DD-API-KEY"},
		[2]string{"appKeyAuth", "DD-APPLICATION-KEY"},
	)

	req, err := c.apiClient.PrepareRequest(
		requestContext,
		baseURL+path,
		method,
		body,
		headerParams,
		url.Values{},
		url.Values{},
		nil,
	)
	if err != nil {
		return fmt.Errorf("prepare datadog deployment gates request: %w", err)
	}

	resp, err := c.apiClient.CallAPI(req)
	if err != nil {
		return fmt.Errorf("call datadog deployment gates API: %w", err)
	}
	if resp == nil {
		return errors.New("datadog deployment gates API returned a nil response")
	}

	responseBody, err := datadog.ReadBody(resp)
	if err != nil {
		return fmt.Errorf("read datadog deployment gates API response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("datadog deployment gates API returned %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}

	if err := c.apiClient.Decode(out, responseBody, resp.Header.Get("Content-Type")); err != nil {
		return fmt.Errorf("decode datadog deployment gates API response: %w", err)
	}

	return nil
}

func (c *datadogDeploymentGatesClient) requestContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	ctx = context.WithValue(ctx, datadog.ContextAPIKeys, c.apiKeys)
	if len(c.serverVariables) > 0 {
		ctx = context.WithValue(ctx, datadog.ContextServerVariables, c.serverVariables)
	}

	return ctx
}

func normalizeDatadogSite(site string) string {
	trimmed := strings.TrimSpace(site)
	if trimmed == "" {
		return defaultDatadogSite
	}

	return strings.TrimPrefix(trimmed, "api.")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
