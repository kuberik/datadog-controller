# Datadog Controller

A Kubernetes controller for managing Datadog monitors and creating corresponding kuberik health checks.

## Overview

The Datadog Controller provides a way to monitor the health of DatadogMonitor resources and create corresponding kuberik health checks that can be used in rollouts and other Kubernetes operations.

## Features

- **MonitorCheck**: Automatically monitors DatadogMonitor resources based on label selectors
- **Health Check Integration**: Creates and manages kuberik health checks based on monitor health status
- **Label-based Selection**: Flexible label selector matching for DatadogMonitor resources
- **Configurable Intervals**: Customizable check intervals and timeouts
- **Status Tracking**: Comprehensive status reporting including healthy/unhealthy monitor counts

## MonitorCheck

The MonitorCheck resource allows you to:

1. **Match DatadogMonitor resources** using label selectors
2. **Monitor health status** of all matched monitors
3. **Create kuberik health checks** that reflect the overall health status
4. **Track statistics** including matched monitors count and health status

### Example Configuration

```yaml
apiVersion: kuberik.com/v1alpha1
kind: MonitorCheck
metadata:
  name: my-app-monitor-check
  namespace: default
spec:
  # Label selector to match DatadogMonitor resources
  labelSelector:
    matchLabels:
      app: my-app
      environment: production

  # Name of the kuberik health check to create
  healthCheckName: my-app-monitor-health

  # Optional: namespace where the health check should be created
  healthCheckNamespace: default

  # Optional: interval between health check evaluations (default: 30s)
  checkInterval: "30s"

  # Optional: timeout for health check evaluation (default: 10s)
  timeout: "10s"

  # Optional: number of retries before marking as unhealthy (default: 3)
  retryCount: 3
```

### How It Works

1. **Discovery**: The controller finds all DatadogMonitor resources that match the specified label selector
2. **Health Evaluation**: It evaluates the health status of each matched monitor
3. **Health Check Creation**: Creates or updates a kuberik health check based on the overall monitor health
4. **Status Updates**: Continuously updates the MonitorCheck status with current statistics

### Status Information

The MonitorCheck status provides:

- `matchedMonitorsCount`: Total number of DatadogMonitor resources that match the label selector
- `healthyMonitorsCount`: Number of healthy monitors
- `unhealthyMonitorsCount`: Number of unhealthy monitors
- `lastCheckTime`: Timestamp of the last health check evaluation
- `healthCheckRef`: Reference to the created kuberik health check

## Installation

### Prerequisites

- Kubernetes cluster
- kubectl configured
- Datadog Operator installed (for DatadogMonitor resources)

### Install the Controller

```bash
# Install CRDs
kubectl apply -f config/crd/bases/

# Install the controller
kubectl apply -k config/default/
```

## Usage

### 1. Create a MonitorCheck

```bash
kubectl apply -f config/samples/v1alpha1_monitorcheck.yaml
```

### 2. Monitor the Status

```bash
kubectl get monitorcheck monitorcheck-sample -o yaml
```

### 3. Check the Created Health Check

The controller will automatically create a kuberik health check that you can use in your rollouts.

## Development

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Running Locally

```bash
make run
```

## Architecture

The controller follows the standard Kubernetes controller pattern:

1. **Reconciliation Loop**: Continuously reconciles MonitorCheck resources
2. **Label Selector Matching**: Uses Kubernetes label selectors to find relevant DatadogMonitor resources
3. **Health Status Evaluation**: Determines the health status of matched monitors
4. **Health Check Management**: Creates and updates kuberik health checks
5. **Status Updates**: Maintains current status information

## Future Enhancements

- **Direct Datadog API Integration**: Real-time monitoring using Datadog API
- **Advanced Health Logic**: Configurable health evaluation rules
- **Metrics and Alerting**: Prometheus metrics and alerting integration
- **Webhook Support**: Configurable webhooks for health status changes

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## License

Apache 2.0
