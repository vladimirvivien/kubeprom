# kubeprom

A lightweight CLI tool for executing PromQL queries against native Kubernetes component metrics without requiring external monitoring addons.

## Overview

kubeprom provides a simple command-line interface for collecting metrics directly from Kubernetes components (API server, etcd, kubelet, scheduler, etc.) and executing PromQL queries against them using an in-memory time-series database.

**Key Features:**
- **Direct Metrics Access**: Scrapes metrics directly from native Kubernetes components
- **PromQL Querying**: Full PromQL support using Prometheus query engine
- **Security-First**: Uses Kubernetes RBAC for proper access control
- **Simple Interface**: Single command with PromQL query parameter
- **In-Memory TSDB**: Fast in-memory time-series database for query execution

## Installation

### Prerequisites

- Go 1.24.2 or later
- Access to a Kubernetes cluster
- Valid kubeconfig file
- Appropriate RBAC permissions (see RBAC Setup)

### Build from Source

```bash
# Clone the repository
git clone <repository-url>
cd kubeprom

# Install dependencies
go mod tidy

# Build the binary
go build -o kubeprom .

# Make it executable and optionally move to PATH
chmod +x kubeprom
sudo mv kubeprom /usr/local/bin/  # Optional
```

## RBAC Setup

kubeprom requires specific Kubernetes permissions to access component metrics. Apply the included RBAC manifests:

```bash
# Apply basic RBAC permissions
kubectl apply -f rbac.yaml

# Verify the ServiceAccount was created
kubectl get serviceaccount kubeprom

# Verify the ClusterRole was created
kubectl get clusterrole kubeprom-metrics-reader

# Verify the ClusterRoleBinding was created
kubectl get clusterrolebinding kubeprom-metrics-reader
```

### Required Permissions

The RBAC configuration grants the following permissions:

- **Core Resources**: Access to nodes, pods, endpoints, services
- **Metrics Endpoints**: Access to `/metrics`, `/metrics/cadvisor`, `/metrics/resource`
- **API Server Metrics**: Access to API server `/metrics` endpoint
- **Authentication**: Token review and subject access review capabilities

## Usage

### Basic Syntax

```bash
kubeprom -query "<promql_query>" [options]
```

### Command Line Options

```bash
-query string
    PromQL query to execute (required)

-kubeconfig string
    Path to kubeconfig file (default: ~/.kube/config)

-insecure-tls
    Skip TLS certificate verification (use with caution)

-debug
    Show debug information during execution
```

### Examples

#### Basic Metric Queries

```bash
# Get current running pod count
kubeprom -query "kubelet_running_pods"

# Get API server request total
kubeprom -query "apiserver_request_total"

# Get container memory usage
kubeprom -query "container_memory_usage_bytes"

# Get node CPU usage
kubeprom -query "node_cpu_usage_seconds_total"
```

#### PromQL Functions

```bash
# Calculate request rate over 5 minutes
kubeprom -query "rate(apiserver_request_total[5m])"

# Sum memory usage across all containers
kubeprom -query "sum(container_memory_usage_bytes)"

# Average CPU usage by node
kubeprom -query "avg by (node) (node_cpu_usage_seconds_total)"

# Count running pods per namespace
kubeprom -query "count by (namespace) (kubelet_running_pods)"
```

#### Label Filtering

```bash
# Filter by specific labels
kubeprom -query 'apiserver_request_total{method="GET"}'

# Multiple label filters
kubeprom -query 'container_memory_usage_bytes{namespace="kube-system",pod=~".*etcd.*"}'

# Regular expressions
kubeprom -query 'kubelet_http_requests_total{path=~"/metrics.*"}'
```

#### Advanced Queries

```bash
# Top 5 memory consuming containers
kubeprom -query "topk(5, container_memory_usage_bytes)"

# 95th percentile of request duration
kubeprom -query "histogram_quantile(0.95, apiserver_request_duration_seconds_bucket)"

# Memory usage percentage
kubeprom -query "(container_memory_usage_bytes / container_memory_limit_bytes) * 100"
```

### Debug Mode

Use `-debug` flag to see detailed information about metric collection:

```bash
kubeprom -query "kubelet_running_pods" -debug
```

Debug output includes:
- Component collection progress
- Number of metric families collected
- Query execution details

### Insecure TLS

For development or testing environments with self-signed certificates:

```bash
kubeprom -query "kubelet_running_pods" -insecure-tls
```

**Warning**: Only use `-insecure-tls` in trusted environments. Production deployments should use proper TLS verification.

## Output Format

kubeprom displays results in a clean tabular format:

```
Query: kubelet_running_pods
Results: 1 metrics found

METRIC                 LABELS   VALUE      TIMESTAMP
------                 ------   -----      ---------
kubelet_running_pods   {}       8.000000   14:32:15
```

For metrics with labels:

```
Query: apiserver_request_total{method="GET"}
Results: 3 metrics found

METRIC                   LABELS                                    VALUE       TIMESTAMP
------                   ------                                    -----       ---------
apiserver_request_total  {method=GET,path=/api/v1,code=200}       1234.000000 14:32:15
apiserver_request_total  {method=GET,path=/metrics,code=200}      567.000000  14:32:15
apiserver_request_total  {method=GET,path=/healthz,code=200}      89.000000   14:32:15
```

## Data Sources

kubeprom collects metrics from the following Kubernetes components:

| Component | Endpoint | Port | Protocol | Auto-Discovery |
|-----------|----------|------|----------|----------------|
| **API Server** | `/metrics` | 6443 | HTTPS | Via kubeconfig |
| **kubelet** | `/metrics` | 10250 | HTTPS | Via node lookup |
| **kubelet** | `/metrics/cadvisor` | 10250 | HTTPS | Via node lookup |
| **etcd** | `/metrics` | 2381 | HTTPS | Via pod discovery |
| **kube-scheduler** | `/metrics` | 10259 | HTTPS | Via pod discovery |
| **kube-controller-manager** | `/metrics` | 10257 | HTTPS | Via pod discovery |
| **kube-proxy** | `/metrics` | 10249 | HTTP | Via pod discovery |

### Metric Categories

- **API Server**: Request rates, latency, authentication metrics
- **kubelet**: Pod lifecycle, container management, resource usage
- **Node**: CPU, memory, network, filesystem metrics
- **etcd**: Database size, consensus, leader election metrics
- **Scheduler**: Pod scheduling, queue depth, latency metrics
- **Controller Manager**: Work queue, controller loops, leader election
- **kube-proxy**: Network rules, iptables sync performance

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   CLI Input     │    │  Metric Store    │    │  PromQL Engine  │
│                 │    │                  │    │                 │
│ PromQL Query ───┼───▶│ In-Memory TSDB ──┼───▶│ Prometheus      │
│                 │    │                  │    │ Query Engine    │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                │
                                ▼
                       ┌──────────────────┐
                       │ Metrics Collection│
                       │                  │
                       │ • API Server     │
                       │ • kubelet        │
                       │ • etcd           │
                       │ • Scheduler      │
                       │ • Controller Mgr │
                       │ • kube-proxy     │
                       └──────────────────┘
```

### Workflow

1. **Collection**: Scrape metrics from multiple Kubernetes components in parallel
2. **Storage**: Store metrics in an in-memory time-series database
3. **Parsing**: Parse and validate PromQL query using Prometheus parser
4. **Execution**: Execute query using Prometheus query engine
5. **Display**: Format and display results in tabular format

## Troubleshooting

### Permission Denied

```bash
Error: failed to GET https://10.0.0.1:10250/metrics: Forbidden
```

**Solution**: Ensure RBAC is properly configured:
```bash
kubectl apply -f rbac.yaml
kubectl get clusterrolebinding kubeprom-metrics-reader
```

### TLS Certificate Errors

```bash
Error: x509: certificate signed by unknown authority
```

**Solutions**:
1. Use `-insecure-tls` flag (development only)
2. Ensure proper CA certificates are configured
3. Check kubeconfig TLS settings

### Connection Refused

```bash
Error: failed to GET https://node-ip:10250/metrics: connection refused
```

**Causes**:
- kubelet metrics port not exposed
- Network policies blocking access
- Firewall rules blocking port 10250

**Solution**: Verify kubelet configuration and network connectivity

### No Metrics Found

```bash
Query: some_metric_name
Results: 0 metrics found
```

**Solutions**:
1. Use `-debug` flag to see available metrics
2. Check if the component is running and exposing metrics
3. Verify metric name spelling and labels
4. Try a broader query first (e.g., `up` or `kubelet_running_pods`)

### Query Syntax Errors

```bash
Error: invalid PromQL query: parse error at char 15: syntax error
```

**Solution**: Verify PromQL syntax:
- Check parentheses matching
- Verify function names and parameters
- Ensure proper label selector syntax: `{label="value"}`

## Security Considerations

### RBAC Best Practices

- **Principle of Least Privilege**: The included RBAC grants only necessary permissions
- **Namespace Isolation**: Consider namespace-specific roles for multi-tenant clusters
- **Regular Audits**: Periodically review and update RBAC permissions

### TLS Security

- **Always Use TLS**: Avoid `-insecure-tls` in production
- **Certificate Validation**: Ensure proper CA certificate configuration
- **Secure kubeconfig**: Protect kubeconfig files with appropriate file permissions

### Network Security

- **Network Policies**: Ensure network policies allow metrics collection
- **Firewall Rules**: Configure firewalls to allow access to metrics ports
- **VPN/Bastion**: Consider VPN or bastion host access for remote clusters

## Limitations

1. **In-Memory Storage**: Metrics are stored in memory only; no persistence
2. **Single Query**: Executes one query at a time; no batch operations
3. **Component Availability**: Requires direct access to component metrics ports
4. **Network Dependencies**: Needs network access to all monitored components
5. **Memory Usage**: Large clusters may require significant memory for metric storage

## Performance Considerations

- **Memory Usage**: ~1-10MB per 1000 metrics depending on label cardinality
- **Collection Time**: ~5-30 seconds depending on cluster size and network latency
- **Query Performance**: Milliseconds for simple queries, seconds for complex aggregations
- **Concurrent Limits**: Limited by Kubernetes API server rate limits

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure RBAC changes are documented
5. Submit a pull request

## License

[Add your license information here]

## Related Projects

- **kubestats**: Referenced implementation for metrics collection patterns
- **Prometheus**: Query engine and metric format
- **Kubernetes**: Native metrics endpoints and RBAC system