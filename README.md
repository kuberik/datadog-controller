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

## Monitoring

The controller logs all operations including:
- HealthCheck creation and deletion
- Status updates
- Error conditions

Check the controller logs to monitor its operation:
```bash
kubectl logs -n <namespace> deployment/datadog-controller
```
