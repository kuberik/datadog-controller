# Datadog Controller

A Kubernetes controller that automatically creates and manages kuberik HealthCheck resources based on DatadogMonitor resources.

## Overview

This controller watches for DatadogMonitor resources and automatically creates corresponding kuberik HealthCheck resources when a specific annotation is present. When a monitor provides Datadog deployment-gate inputs, the HealthCheck status is driven by the Datadog Deployment Gates API; otherwise it falls back to the DatadogMonitor status.

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

When the monitor has both `service:` and `env:` tags (or equivalent override annotations), the controller evaluates a Datadog deployment gate and maps the gate result to the HealthCheck:

- **pass** -> `Healthy`
- **fail** -> `Unhealthy` (with error timestamp)
- **in_progress** -> `Pending`

If deployment-gate inputs are not configured, the controller falls back to the DatadogMonitor status:

- **OK** -> `Healthy`
- **Alert** -> `Unhealthy` (with error timestamp)
- **Warn/NoData/Skipped/Ignored** -> `Pending`

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
    # Optional deployment-gate overrides:
    # kuberik.com/datadog-gate-identifier: "canary"
    # kuberik.com/datadog-gate-version: "1.2.3"
    # kuberik.com/datadog-gate-apm-primary-tag: "team:platform"
spec:
  name: "High CPU Alert"
  type: "metric alert"
  query: "avg(last_5m):avg:system.cpu.user{*} > 80"
  message: "CPU usage is high"
  tags:
    - "env:production"
    - "service:payments"
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

Optional deployment-gate annotations on the DatadogMonitor:
- `kuberik.com/datadog-gate-service`: Override the Datadog service instead of reading the `service:` tag
- `kuberik.com/datadog-gate-env`: Override the Datadog environment instead of reading the `env:` tag
- `kuberik.com/datadog-gate-identifier`: Override the gate identifier (defaults to `default`)
- `kuberik.com/datadog-gate-version`: Version forwarded to the Datadog gate evaluation request
- `kuberik.com/datadog-gate-apm-primary-tag`: APM primary tag forwarded to the Datadog gate evaluation request

To enable deployment-gate evaluations, set Datadog credentials in the controller environment:
- `DATADOG_API_KEY` or `DD_API_KEY`
- `DATADOG_APP_KEY` or `DD_APP_KEY`
- `DATADOG_SITE` or `DD_SITE` (optional, defaults to `datadoghq.com`)

The install manifests read these from an optional `datadog-credentials` Secret in the controller namespace (keys `api-key`, `app-key`, `site`). One Secret serves every HealthCheck in the cluster:

```bash
kubectl -n kuberik-system create secret generic datadog-credentials \
  --from-literal=api-key=... --from-literal=app-key=...
```

## Standalone Datadog HealthChecks

A HealthCheck with `spec.class: datadog` is evaluated against a Datadog deployment gate directly, with no DatadogMonitor. It takes the same `kuberik.com/datadog-gate-*` annotations, and `service` and `env` are required:

```yaml
apiVersion: kuberik.com/v1alpha1
kind: HealthCheck
metadata:
  name: payments-datadog
  labels:
    app: payments
  annotations:
    kuberik.com/datadog-gate-service: payments
    kuberik.com/datadog-gate-env: prod
    kuberik.com/datadog-gate-version: "${SERVICE_VERSION}"
spec:
  class: datadog
```

The gate is evaluated again whenever:
- the inputs change, for example a new `datadog-gate-version`, or
- the rollout controller resets the HealthCheck for a new deployment or a retry.

Each deployment therefore gets a fresh gate verdict.

## Monitoring

The controller logs all operations including:
- HealthCheck creation and deletion
- Status updates
- Error conditions

Check the controller logs to monitor its operation:
```bash
kubectl logs -n <namespace> deployment/datadog-controller
```
