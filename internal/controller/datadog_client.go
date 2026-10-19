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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

const (
	datadogAPIKeyEnvVar    = "DATADOG_API_KEY"
	datadogAltAPIKeyEnvVar = "DD_API_KEY"
	datadogAppKeyEnvVar    = "DATADOG_APP_KEY"
	datadogAltAppKeyEnvVar = "DD_APP_KEY"
	datadogSiteEnvVar      = "DATADOG_SITE"
	datadogAltSiteEnvVar   = "DD_SITE"
	defaultDatadogSite     = "datadoghq.com"

	monitorListPageSize  = 1000
	incidentSearchPageSz = 100
)

var errDatadogNotConfigured = errors.New("datadog API credentials are not configured")

// datadogMonitorState is the part of a Datadog monitor a HealthCheck is evaluated on.
type datadogMonitorState struct {
	ID           int64
	Name         string
	Tags         []string
	OverallState string
	// Groups maps a group name (e.g. "env:prod,service:api") to its status.
	Groups map[string]string
}

// datadogIncident is an open Datadog incident matching a HealthCheck query.
type datadogIncident struct {
	PublicID int64
	Title    string
}

// datadogAPI is the subset of the Datadog API the HealthCheck classes use.
type datadogAPI interface {
	GetMonitor(ctx context.Context, id int64) (datadogMonitorState, error)
	ListMonitorsByTags(ctx context.Context, tags []string) ([]datadogMonitorState, error)
	SearchOpenIncidents(ctx context.Context, query string) ([]datadogIncident, error)
}

type datadogAPIClient struct {
	monitors  *datadogV1.MonitorsApi
	incidents *datadogV2.IncidentsApi
	apiKey    string
	appKey    string
	site      string
}

// newDatadogAPIFromEnv builds a client from the controller environment, so one
// set of credentials serves every HealthCheck in the cluster.
func newDatadogAPIFromEnv() (datadogAPI, error) {
	apiKey := firstNonEmpty(os.Getenv(datadogAPIKeyEnvVar), os.Getenv(datadogAltAPIKeyEnvVar))
	appKey := firstNonEmpty(os.Getenv(datadogAppKeyEnvVar), os.Getenv(datadogAltAppKeyEnvVar))
	if apiKey == "" || appKey == "" {
		return nil, fmt.Errorf("%w: set %s and %s", errDatadogNotConfigured, datadogAltAPIKeyEnvVar, datadogAltAppKeyEnvVar)
	}

	configuration := datadog.NewConfiguration()
	configuration.SetUnstableOperationEnabled("v2.SearchIncidents", true)
	configuration.HTTPClient.Timeout = 30 * time.Second
	apiClient := datadog.NewAPIClient(configuration)

	return &datadogAPIClient{
		monitors:  datadogV1.NewMonitorsApi(apiClient),
		incidents: datadogV2.NewIncidentsApi(apiClient),
		apiKey:    apiKey,
		appKey:    appKey,
		site: strings.TrimPrefix(strings.TrimPrefix(
			firstNonEmpty(os.Getenv(datadogSiteEnvVar), os.Getenv(datadogAltSiteEnvVar), defaultDatadogSite),
			"https://"), "api."),
	}, nil
}

func (c *datadogAPIClient) requestContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, datadog.ContextAPIKeys, map[string]datadog.APIKey{
		"apiKeyAuth": {Key: c.apiKey},
		"appKeyAuth": {Key: c.appKey},
	})
	return context.WithValue(ctx, datadog.ContextServerVariables, map[string]string{"site": c.site})
}

func (c *datadogAPIClient) GetMonitor(ctx context.Context, id int64) (datadogMonitorState, error) {
	monitor, _, err := c.monitors.GetMonitor(c.requestContext(ctx), id,
		*datadogV1.NewGetMonitorOptionalParameters().WithGroupStates("all"))
	if err != nil {
		return datadogMonitorState{}, fmt.Errorf("get monitor %d: %w", id, err)
	}
	return toMonitorState(monitor), nil
}

func (c *datadogAPIClient) ListMonitorsByTags(ctx context.Context, tags []string) ([]datadogMonitorState, error) {
	var monitors []datadogMonitorState
	for page := int64(0); ; page++ {
		result, _, err := c.monitors.ListMonitors(c.requestContext(ctx), *datadogV1.NewListMonitorsOptionalParameters().
			WithMonitorTags(strings.Join(tags, ",")).
			WithGroupStates("all").
			WithPage(page).
			WithPageSize(monitorListPageSize))
		if err != nil {
			return nil, fmt.Errorf("list monitors with tags %v: %w", tags, err)
		}
		for _, monitor := range result {
			monitors = append(monitors, toMonitorState(monitor))
		}
		if len(result) < monitorListPageSize {
			return monitors, nil
		}
	}
}

func (c *datadogAPIClient) SearchOpenIncidents(ctx context.Context, query string) ([]datadogIncident, error) {
	fullQuery := fmt.Sprintf("state:(active OR stable) AND (%s)", query)
	var incidents []datadogIncident
	for offset := int64(0); ; offset += incidentSearchPageSz {
		result, _, err := c.incidents.SearchIncidents(c.requestContext(ctx), fullQuery,
			*datadogV2.NewSearchIncidentsOptionalParameters().WithPageSize(incidentSearchPageSz).WithPageOffset(offset))
		if err != nil {
			return nil, fmt.Errorf("search incidents %q: %w", fullQuery, err)
		}
		attributes := result.Data.Attributes
		if attributes == nil {
			return incidents, nil
		}
		for _, incident := range attributes.Incidents {
			incidentAttributes := incident.Data.Attributes
			if incidentAttributes == nil {
				continue
			}
			incidents = append(incidents, datadogIncident{
				PublicID: incidentAttributes.GetPublicId(),
				Title:    incidentAttributes.Title,
			})
		}
		if len(attributes.Incidents) < incidentSearchPageSz {
			return incidents, nil
		}
	}
}

func toMonitorState(monitor datadogV1.Monitor) datadogMonitorState {
	state := datadogMonitorState{
		ID:           monitor.GetId(),
		Name:         monitor.GetName(),
		Tags:         monitor.Tags,
		OverallState: string(monitor.GetOverallState()),
		Groups:       map[string]string{},
	}
	if monitor.State != nil {
		for name, group := range monitor.State.Groups {
			state.Groups[name] = string(group.GetStatus())
		}
	}
	return state
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
