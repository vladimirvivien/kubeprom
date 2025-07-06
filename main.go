package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	var kubeconfig string
	var insecureTLS bool
	var debug bool
	var query string

	// Set up command line flags
	if home := homedir.HomeDir(); home != "" {
		flag.StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), 
			"(optional) absolute path to the kubeconfig file")
	} else {
		flag.StringVar(&kubeconfig, "kubeconfig", "", 
			"absolute path to the kubeconfig file")
	}
	
	flag.BoolVar(&insecureTLS, "insecure-tls", false, 
		"Skip TLS certificate verification for component connections (use with caution)")
	flag.BoolVar(&debug, "debug", false, 
		"Show debug information")
	flag.StringVar(&query, "query", "", 
		"PromQL query to execute (required)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] -query <promql_query>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "kubeprom - Kubernetes Native Metrics with PromQL\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -query \"kubelet_running_pods\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -query \"rate(apiserver_request_total[5m])\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -query \"container_memory_usage_bytes\" -insecure-tls\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Validate required query parameter
	if query == "" {
		fmt.Fprintf(os.Stderr, "Error: -query parameter is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// Build Kubernetes configuration
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building kubeconfig: %v\n", err)
		os.Exit(1)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Execute the PromQL query
	if err := executePromQLQuery(ctx, kubeConfig, query, insecureTLS, debug); err != nil {
		fmt.Fprintf(os.Stderr, "Error executing query: %v\n", err)
		os.Exit(1)
	}
}

// executePromQLQuery handles the main PromQL query execution workflow
func executePromQLQuery(ctx context.Context, kubeConfig interface{}, query string, insecureTLS, debug bool) error {
	if debug {
		fmt.Printf("Debug: Executing PromQL query: %s\n", query)
	}

	// Create the metric store
	store := NewMetricStore()

	// Collect metrics from all available components
	fmt.Println("Collecting metrics from Kubernetes components...")
	if err := collectAllMetrics(ctx, store, kubeConfig, insecureTLS, debug); err != nil {
		return fmt.Errorf("failed to collect metrics: %w", err)
	}

	// Execute the PromQL query
	results, err := store.ExecutePromQL(ctx, query)
	if err != nil {
		return fmt.Errorf("query execution failed: %w", err)
	}

	// Display results in tabular format
	displayResults(query, results)
	return nil
}

// displayResults outputs the query results in a formatted table
func displayResults(query string, results []MetricResult) {
	fmt.Printf("\nQuery: %s\n", query)
	fmt.Printf("Results: %d metrics found\n\n", len(results))

	if len(results) == 0 {
		fmt.Println("No metrics found matching the query.")
		return
	}

	// Create tabwriter for aligned output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()

	// Print header
	fmt.Fprintln(w, "METRIC\tLABELS\tVALUE\tTIMESTAMP")
	fmt.Fprintln(w, "------\t------\t-----\t---------")

	// Print results
	for _, result := range results {
		metricName := result.MetricName
		
		// Build label string (excluding __name__)
		var labelPairs []string
		for k, v := range result.Labels {
			if k != "__name__" {
				labelPairs = append(labelPairs, fmt.Sprintf("%s=%s", k, v))
			}
		}
		labelStr := strings.Join(labelPairs, ",")
		if labelStr == "" {
			labelStr = "{}"
		} else {
			labelStr = "{" + labelStr + "}"
		}

		timestamp := time.UnixMilli(result.Timestamp).Format("15:04:05")
		
		fmt.Fprintf(w, "%s\t%s\t%.6f\t%s\n", 
			metricName, labelStr, result.Value, timestamp)
	}
}