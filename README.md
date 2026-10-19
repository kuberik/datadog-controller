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
- The HealthCheck is named `datadog-check-{monitor-name}`
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

Unhealthy while any open (active or stable) Datadog incident matches a search query:

```yaml
metadata:
  annotations:
    healthcheck.kuberik.com/datadog-incident-query: 'services:api'
spec:
  class: datadog-incidents
```

### Credentials

The controller reads one set of credentials for the whole cluster from its environment: `DD_API_KEY`, `DD_APP_KEY`, and optionally `DD_SITE` (default `datadoghq.com`). The install manifests take them from an optional `datadog-credentials` Secret in the controller namespace, with keys `api-key`, `app-key` and `site`:

```bash
kubectl -n kuberik-system create secret generic datadog-credentials \
  --from-literal=api-key=... --from-literal=app-key=...
```

The application key needs the `monitors_read` scope, plus `incident_read` for `datadog-incidents`. Without credentials, these HealthChecks stay `Pending`.

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
