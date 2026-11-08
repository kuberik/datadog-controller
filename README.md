# Datadog Controller

A Kubernetes controller that automatically creates and manages kuberik HealthCheck resources based on DatadogMonitor resources.

## Overview

This controller watches for DatadogMonitor resources and automatically creates corresponding kuberik HealthCheck resources when a specific annotation is present. The HealthCheck status is then continuously updated based on the DatadogMonitor's health state.

## How It Works

### 1. Annotation-Based HealthCheck Creation

To enable automatic HealthCheck creation for a DatadogMonitor, simply add the following annotation:

```yaml
metadata:
  annotations:
    kuberik.com/health-check: "true"
```

### 2. Automatic HealthCheck Management

When the annotation is present:
- A kuberik HealthCheck resource is automatically created
- The HealthCheck is named `datadog-check-{monitor-name}` and has `spec.class: datadog-monitor-crd`
- Owner references are set to link the HealthCheck to the DatadogMonitor
- The HealthCheck is placed in the same namespace as the DatadogMonitor

### 3. Status Synchronization

The controller continuously monitors the DatadogMonitor status and updates the HealthCheck accordingly:

- **OK** → `Healthy`
- **Alert** → `Unhealthy` (with error timestamp)
- **Warn/NoData/Skipped/Ignored** → `Pending`

### 4. Cleanup

When the annotation is removed:
- The associated HealthCheck is automatically deleted
- This ensures clean resource management

## Example Usage

```yaml
apiVersion: datadoghq.com/v1alpha1
kind: DatadogMonitor
metadata:
  name: my-monitor
  namespace: production
  annotations:
    kuberik.com/health-check: "true"
spec:
  name: "High CPU Alert"
  type: "metric alert"
  query: "avg(last_5m):avg:system.cpu.user{*} > 80"
  message: "CPU usage is high"
```

This will automatically create a HealthCheck named `datadog-check-my-monitor` in the `production` namespace.

## Architecture

The system consists of two main controllers:

1. **DatadogMonitor Controller**: Watches DatadogMonitor resources and creates/manages HealthCheck resources
2. **HealthCheck Controller**: Updates HealthCheck status based on DatadogMonitor health state

## Prerequisites

- Kubernetes cluster with Datadog Operator installed
- kuberik Rollout Controller with HealthCheck CRD
- This controller deployed and running

## Installation

1. Deploy the controller to your cluster
2. Create DatadogMonitor resources with the `kuberik.com/health-check: "true"` annotation
3. The controller will automatically create and manage HealthCheck resources

## Configuration

The controller uses the following annotation key:
- `kuberik.com/health-check`: Set to `"true"` to enable HealthCheck creation

## Datadog API HealthChecks

HealthChecks of the classes below are evaluated by querying the Datadog API directly. They need no Datadog Operator and no DatadogMonitor resources. Every class polls Datadog every 60s; `healthcheck.kuberik.com/requeue-interval` changes that (minimum 10s).

### `datadog`: monitor status

Selects monitors by ID and/or monitor tags, and is Unhealthy while any selected monitor is alerting. Group tags then narrow each monitor to the groups that concern this HealthCheck:

```yaml
apiVersion: kuberik.com/v1alpha1
kind: HealthCheck
metadata:
  name: api-datadog
  labels:
    app: api
  annotations:
    # Monitors carrying all of these tags.
    healthcheck.kuberik.com/datadog-monitor-tags: "slo:http-success,env:prod"
    # And/or explicit monitor IDs.
    # healthcheck.kuberik.com/datadog-monitor-ids: "123456,234567"
    # Only the service:api groups of monitors grouped by service.
    healthcheck.kuberik.com/datadog-group-tags: "service:api,env:prod"
spec:
  class: datadog
```

- A group counts unless it has one of the group-tag keys with a different value. So for a monitor grouped by `service`, only the `service:api` group counts, while a monitor not grouped by `service` is evaluated as a whole. A monitor without groups uses its overall state.
- **Alert** makes the HealthCheck `Unhealthy`; every other monitor state is `Healthy`.
- If no monitor carries the tags, or no group matches, the HealthCheck is `Healthy` and its message says nothing was evaluated. Tagging a monitor is how it opts in, so adding or removing monitors never blocks a deploy. A monitor ID that doesn't exist, or a Datadog API error, keeps the HealthCheck `Pending`.

### `datadog-incidents`: open incidents

Unhealthy while any open (active or stable) Datadog incident matches a search query. The query is appended as `state:(active OR stable) AND <query>`. Write it the way Datadog's incident search expects: facets joined with `AND`, and parentheses only around one facet's values, e.g. `services:api AND severity:(SEV-1 OR SEV-2)`.

```yaml
metadata:
  annotations:
    healthcheck.kuberik.com/datadog-incident-query: 'services:api'
spec:
  class: datadog-incidents
```

### Observations

Besides the status and message, these HealthChecks write typed `status.observations` that the dashboard draws as a figure (requires a rollout-controller that has the field):

- `datadog` and the `DatadogMonitor` resource path write one observation per monitor (failing first, at most 16). For `metric alert` and `query alert` monitors whose query looks like `avg(last_5m):<metric> > 80` this is a **Threshold**: `limit` is `options.thresholds.critical` (or the number in the query), `direction` is `Above` for `>`/`>=` and `Below` for `<`/`<=`, `window` is the evaluation window (`last_5m` becomes `5m`), and `healthy` is true for OK, false for Alert or Warn, and unset for no data. Every other case is a **State** carrying only the name and `healthy`: service checks, composite, log/APM/RUM monitors, `change()`/`pct_change()` queries, `==`/`!=` comparators, or a monitor whose value could not be fetched.
- Datadog's monitor API does not expose the value a monitor last evaluated. The controller therefore runs the monitor's own metric query over its window (`GET /api/v1/query`, the aggregator `avg`/`sum`/`min`/`max`/`last` applied client-side), narrowed to the series matching the group tags; with several series it reports the one closest to breaching. This needs the `timeseries_query` application key scope. On the `DatadogMonitor` resource path it only happens when the controller has credentials; otherwise the observation is a State.
- Numbers are plain decimals (no exponent). `unit` is set only for time units Datadog reports (`Milliseconds`, `Seconds`); Datadog percentages are 0..100, not the 0..1 ratio `Percent` means, so they have no unit.
- `datadog-incidents` writes one **Count** named `open incidents` with the number of matching open incidents; `healthy` is true when it is 0.
- `observedTime` changes only when the observation does, so a steady value does not rewrite the HealthCheck on every poll.

### Credentials

The controller reads one set of credentials for the whole cluster from its environment: `DD_API_KEY`, `DD_APP_KEY`, and optionally `DD_SITE` (default `datadoghq.com`). The install manifests take them from an optional `datadog-credentials` Secret in the controller namespace, with keys `api-key`, `app-key` and `site`:

```bash
kubectl -n kuberik-system create secret generic datadog-credentials \
  --from-literal=api-key=... --from-literal=app-key=...
```

The application key needs the `monitors_read` scope, plus `incident_read` for `datadog-incidents` and `timeseries_query` for observation values. Without credentials, these HealthChecks stay `Pending`.

### Rollout semantics

While a HealthCheck is `Unhealthy`, `lastErrorTime` keeps the time the failure was first seen. On a new deployment or a retry, the rollout controller resets the HealthCheck, so a failure still present after the reset is stamped with a new time and fails the bake.

## Monitoring

The controller logs all operations including:
- HealthCheck creation and deletion
- Status updates
- Error conditions

Check the controller logs to monitor its operation:
```bash
kubectl logs -n <namespace> deployment/datadog-controller
```
