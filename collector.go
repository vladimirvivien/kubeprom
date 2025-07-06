package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// collectAllMetrics collects metrics from all available Kubernetes components
func collectAllMetrics(ctx context.Context, store *MetricStore, kubeConfig interface{}, insecureTLS, debug bool) error {
	config := kubeConfig.(*rest.Config)
	
	// Collect from multiple components in parallel
	components := []string{"apiserver", "kubelet", "node", "scheduler", "controller-manager"}
	
	for _, component := range components {
		if debug {
			fmt.Printf("Debug: Collecting metrics from %s...\n", component)
		}
		
		families, err := collectComponentMetrics(ctx, config, component, "", insecureTLS, debug)
		if err != nil {
			if debug {
				fmt.Printf("Warning: Failed to collect metrics from %s: %v\n", component, err)
			}
			continue // Continue with other components even if one fails
		}
		
		if families != nil {
			store.AddMetricFamilies(families)
			if debug {
				fmt.Printf("Debug: Added %d metric families from %s\n", len(families), component)
			}
		}
	}
	
	return nil
}

// collectComponentMetrics collects metrics from a specific Kubernetes component
func collectComponentMetrics(ctx context.Context, config *rest.Config, component, componentName string, insecureTLS, debug bool) (map[string]*dto.MetricFamily, error) {
	switch component {
	case "apiserver":
		return collectAPIServerMetrics(ctx, config)
	case "kubelet":
		return collectKubeletMetrics(ctx, config, componentName, insecureTLS)
	case "node":
		return collectNodeMetrics(ctx, config, componentName, insecureTLS)
	case "etcd":
		return collectEtcdMetrics(ctx, config, componentName, insecureTLS)
	case "scheduler":
		return collectSchedulerMetrics(ctx, config, componentName, insecureTLS)
	case "controller-manager":
		return collectControllerManagerMetrics(ctx, config, componentName, insecureTLS)
	case "kube-proxy":
		return collectKubeProxyMetrics(ctx, config, componentName, insecureTLS)
	default:
		return nil, fmt.Errorf("unsupported component: %s", component)
	}
}

// collectAPIServerMetrics collects metrics from the Kubernetes API server
func collectAPIServerMetrics(ctx context.Context, config *rest.Config) (map[string]*dto.MetricFamily, error) {
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP client: %v", err)
	}

	metricsURL := config.Host + "/metrics"
	return scrapeMetrics(ctx, httpClient, metricsURL)
}

// collectKubeletMetrics collects metrics from kubelet using RESTClient
func collectKubeletMetrics(ctx context.Context, config *rest.Config, nodeName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Get the first node if no specific node name provided
	if nodeName == "" {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %v", err)
		}
		if len(nodes.Items) == 0 {
			return nil, fmt.Errorf("no nodes found")
		}
		nodeName = nodes.Items[0].Name
	}

	// Verify node exists
	_, err = clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting node %s: %v", nodeName, err)
	}

	// Use RESTClient to access kubelet metrics via node proxy
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("metrics").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get kubelet metrics via node proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// collectNodeMetrics collects node resource metrics from kubelet using RESTClient
func collectNodeMetrics(ctx context.Context, config *rest.Config, nodeName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Get the first node if no specific node name provided
	if nodeName == "" {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("listing nodes: %v", err)
		}
		if len(nodes.Items) == 0 {
			return nil, fmt.Errorf("no nodes found")
		}
		nodeName = nodes.Items[0].Name
	}

	// Verify node exists
	_, err = clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting node %s: %v", nodeName, err)
	}

	// Use RESTClient to access cAdvisor metrics via node proxy
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("metrics/cadvisor").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get cAdvisor metrics via node proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// collectEtcdMetrics collects metrics from etcd using pod proxy
func collectEtcdMetrics(ctx context.Context, config *rest.Config, componentName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Find etcd pod in kube-system namespace
	pods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "component=etcd",
	})
	if err != nil {
		return nil, fmt.Errorf("listing etcd pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no etcd pods found")
	}

	etcdPod := pods.Items[0]

	// Use RESTClient to access etcd metrics via pod proxy
	// Default etcd metrics port is 2381
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Namespace("kube-system").
		Resource("pods").
		Name(etcdPod.Name + ":2381").
		SubResource("proxy").
		Suffix("metrics").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get etcd metrics via pod proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// collectSchedulerMetrics collects metrics from kube-scheduler using pod proxy
func collectSchedulerMetrics(ctx context.Context, config *rest.Config, componentName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Find scheduler pod in kube-system namespace
	pods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "component=kube-scheduler",
	})
	if err != nil {
		return nil, fmt.Errorf("listing scheduler pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no kube-scheduler pods found")
	}

	schedulerPod := pods.Items[0]

	// Use RESTClient to access scheduler metrics via pod proxy
	// Default scheduler metrics port is 10259
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Namespace("kube-system").
		Resource("pods").
		Name(schedulerPod.Name + ":10259").
		SubResource("proxy").
		Suffix("metrics").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get scheduler metrics via pod proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// collectControllerManagerMetrics collects metrics from kube-controller-manager using pod proxy
func collectControllerManagerMetrics(ctx context.Context, config *rest.Config, componentName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Find controller manager pod in kube-system namespace
	pods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "component=kube-controller-manager",
	})
	if err != nil {
		return nil, fmt.Errorf("listing controller manager pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no kube-controller-manager pods found")
	}

	controllerPod := pods.Items[0]

	// Use RESTClient to access controller manager metrics via pod proxy
	// Default controller manager metrics port is 10257
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Namespace("kube-system").
		Resource("pods").
		Name(controllerPod.Name + ":10257").
		SubResource("proxy").
		Suffix("metrics").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get controller manager metrics via pod proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// collectKubeProxyMetrics collects metrics from kube-proxy using pod proxy
func collectKubeProxyMetrics(ctx context.Context, config *rest.Config, componentName string, insecureTLS bool) (map[string]*dto.MetricFamily, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %v", err)
	}

	// Find kube-proxy pods in kube-system namespace
	pods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=kube-proxy",
	})
	if err != nil {
		return nil, fmt.Errorf("listing kube-proxy pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no kube-proxy pods found")
	}

	proxyPod := pods.Items[0]

	// Use RESTClient to access kube-proxy metrics via pod proxy
	// Default kube-proxy metrics port is 10249
	restClient := clientset.CoreV1().RESTClient()
	result := restClient.Get().
		Namespace("kube-system").
		Resource("pods").
		Name(proxyPod.Name + ":10249").
		SubResource("proxy").
		Suffix("metrics").
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, fmt.Errorf("failed to get kube-proxy metrics via pod proxy: %v", err)
	}

	rawBody, err := result.Raw()
	if err != nil {
		return nil, fmt.Errorf("failed to get raw response body: %v", err)
	}

	return parseMetricsBody(rawBody)
}

// getNodeAddress returns the node's IP address
func getNodeAddress(node *v1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			return addr.Address
		}
	}
	// Fallback to external IP if internal IP not found
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeExternalIP {
			return addr.Address
		}
	}
	return ""
}

// parseMetricsBody parses raw metrics response body into MetricFamily map
func parseMetricsBody(body []byte) (map[string]*dto.MetricFamily, error) {
	var parser expfmt.TextParser
	return parser.TextToMetricFamilies(strings.NewReader(string(body)))
}

// scrapeMetrics performs GET request and returns parsed metric families
func scrapeMetrics(ctx context.Context, client *http.Client, url string) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return nil, fmt.Errorf("request cancelled for %s", url)
		}
		return nil, fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("failed GET %s: status code %d (%s)\nResponse snippet: %s", 
			url, resp.StatusCode, resp.Status, string(bodyBytes))
	}

	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response body from %s: %w", url, err)
	}

	return metricFamilies, nil
}