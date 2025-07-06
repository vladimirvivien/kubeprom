package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
)

// MetricResult represents a single metric result
type MetricResult struct {
	MetricName string
	Labels     map[string]string
	Value      float64
	Timestamp  int64
}

// MetricStore is the in-memory time-series database
type MetricStore struct {
	mutex   sync.RWMutex
	series  map[string]*TimeSeries
	engine  *promql.Engine
	storage *InMemoryStorage
}

// TimeSeries represents a single time series with its samples
type TimeSeries struct {
	SeriesLabels labels.Labels
	Samples      []Sample
}

// Sample represents a single data point
type Sample struct {
	Timestamp int64
	Value     float64
}

// InMemoryStorage implements storage.Storage interface
type InMemoryStorage struct {
	store *MetricStore
}

// InMemoryQuerier implements storage.Querier interface
type InMemoryQuerier struct {
	store *MetricStore
	mint  int64
	maxt  int64
}

// InMemorySeriesSet implements storage.SeriesSet interface
type InMemorySeriesSet struct {
	series  []*TimeSeries
	current int
}

// InMemorySeriesIterator implements chunkenc.Iterator interface
type InMemorySeriesIterator struct {
	samples []Sample
	current int
}

// NewMetricStore creates a new in-memory metric store with PromQL engine
func NewMetricStore() *MetricStore {
	store := &MetricStore{
		series: make(map[string]*TimeSeries),
	}
	
	// Create storage wrapper
	store.storage = &InMemoryStorage{store: store}
	
	// Create PromQL engine
	opts := promql.EngineOpts{
		MaxSamples:    50000000,
		Timeout:       5 * time.Minute,
		LookbackDelta: 5 * time.Minute,
	}
	store.engine = promql.NewEngine(opts)
	
	return store
}

// AddMetricFamilies adds metric families to the store
func (s *MetricStore) AddMetricFamilies(families map[string]*dto.MetricFamily) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	
	timestamp := time.Now().UnixMilli()
	
	for metricName, family := range families {
		for _, metric := range family.Metric {
			// Create labels for this metric
			lbls := make(labels.Labels, 0, len(metric.Label)+1)
			lbls = append(lbls, labels.Label{Name: "__name__", Value: metricName})
			
			// Add metric labels
			for _, label := range metric.Label {
				lbls = append(lbls, labels.Label{
					Name:  label.GetName(),
					Value: label.GetValue(),
				})
			}
			
			// Sort labels for consistent series identification
			sort.Slice(lbls, func(i, j int) bool {
				return lbls[i].Name < lbls[j].Name
			})
			
			// Create series key
			seriesKey := lbls.String()
			
			// Get or create time series
			series, exists := s.series[seriesKey]
			if !exists {
				series = &TimeSeries{
					SeriesLabels: lbls,
					Samples:      make([]Sample, 0),
				}
				s.series[seriesKey] = series
			}
			
			// Extract value based on metric type
			var value float64
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				if metric.Counter != nil {
					value = metric.Counter.GetValue()
				}
			case dto.MetricType_GAUGE:
				if metric.Gauge != nil {
					value = metric.Gauge.GetValue()
				}
			case dto.MetricType_HISTOGRAM:
				if metric.Histogram != nil {
					value = float64(metric.Histogram.GetSampleCount())
				}
			case dto.MetricType_SUMMARY:
				if metric.Summary != nil {
					value = float64(metric.Summary.GetSampleCount())
				}
			case dto.MetricType_UNTYPED:
				if metric.Untyped != nil {
					value = metric.Untyped.GetValue()
				}
			}
			
			// Add sample to series
			series.Samples = append(series.Samples, Sample{
				Timestamp: timestamp,
				Value:     value,
			})
			
			// Keep only last 1000 samples per series to limit memory usage
			if len(series.Samples) > 1000 {
				series.Samples = series.Samples[len(series.Samples)-1000:]
			}
		}
	}
}

// ExecutePromQL executes a PromQL query and returns results
func (s *MetricStore) ExecutePromQL(ctx context.Context, query string) ([]MetricResult, error) {
	// Validate the query first
	_, err := parser.ParseExpr(query)
	if err != nil {
		return nil, fmt.Errorf("invalid PromQL query: %w", err)
	}
	
	// Execute the query using the Prometheus engine
	q, err := s.engine.NewInstantQuery(ctx, s.storage, nil, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to create query: %w", err)
	}
	defer q.Close()
	
	result := q.Exec(ctx)
	if result.Err != nil {
		return nil, fmt.Errorf("query execution failed: %w", result.Err)
	}
	
	// Convert result to MetricResult slice
	return s.convertPromQLResult(*result)
}

// convertPromQLResult converts Prometheus query result to MetricResult slice
func (s *MetricStore) convertPromQLResult(result promql.Result) ([]MetricResult, error) {
	var results []MetricResult
	
	switch v := result.Value.(type) {
	case promql.Vector:
		for _, sample := range v {
			metricResult := MetricResult{
				MetricName: sample.Metric.Get("__name__"),
				Labels:     make(map[string]string),
				Value:      sample.F,
				Timestamp:  sample.T,
			}
			
			// Convert labels
			for _, label := range sample.Metric {
				metricResult.Labels[label.Name] = label.Value
			}
			
			results = append(results, metricResult)
		}
	case promql.Scalar:
		results = append(results, MetricResult{
			MetricName: "scalar",
			Labels:     map[string]string{},
			Value:      v.V,
			Timestamp:  v.T,
		})
	case promql.Matrix:
		for _, series := range v {
			metricName := series.Metric.Get("__name__")
			labels := make(map[string]string)
			for _, label := range series.Metric {
				labels[label.Name] = label.Value
			}
			
			// For matrix results, return the latest value from each series
			if len(series.Floats) > 0 {
				latest := series.Floats[len(series.Floats)-1]
				results = append(results, MetricResult{
					MetricName: metricName,
					Labels:     labels,
					Value:      latest.F,
					Timestamp:  latest.T,
				})
			}
		}
	default:
		return nil, fmt.Errorf("unsupported result type: %T", v)
	}
	
	return results, nil
}

// Storage interface implementation
func (s *InMemoryStorage) Querier(mint, maxt int64) (storage.Querier, error) {
	return &InMemoryQuerier{
		store: s.store,
		mint:  mint,
		maxt:  maxt,
	}, nil
}

func (s *InMemoryStorage) StartTime() (int64, error) {
	return 0, nil
}

func (s *InMemoryStorage) Appender(ctx context.Context) storage.Appender {
	return nil // We don't support appending via storage interface
}

func (s *InMemoryStorage) Close() error {
	return nil
}

// Querier interface implementation
func (q *InMemoryQuerier) Select(ctx context.Context, sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	q.store.mutex.RLock()
	defer q.store.mutex.RUnlock()
	
	var matchingSeries []*TimeSeries
	
	for _, series := range q.store.series {
		if q.matchesLabels(series.SeriesLabels, matchers) {
			// Filter samples by time range
			filteredSeries := &TimeSeries{
				SeriesLabels: series.SeriesLabels,
				Samples:      make([]Sample, 0),
			}
			
			for _, sample := range series.Samples {
				if sample.Timestamp >= q.mint && sample.Timestamp <= q.maxt {
					filteredSeries.Samples = append(filteredSeries.Samples, sample)
				}
			}
			
			if len(filteredSeries.Samples) > 0 {
				matchingSeries = append(matchingSeries, filteredSeries)
			}
		}
	}
	
	if sortSeries {
		sort.Slice(matchingSeries, func(i, j int) bool {
			return labels.Compare(matchingSeries[i].SeriesLabels, matchingSeries[j].SeriesLabels) < 0
		})
	}
	
	return &InMemorySeriesSet{
		series:  matchingSeries,
		current: -1,
	}
}

func (q *InMemoryQuerier) matchesLabels(seriesLabels labels.Labels, matchers []*labels.Matcher) bool {
	for _, matcher := range matchers {
		labelValue := seriesLabels.Get(matcher.Name)
		if !matcher.Matches(labelValue) {
			return false
		}
	}
	return true
}

func (q *InMemoryQuerier) LabelValues(ctx context.Context, name string, hints *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	q.store.mutex.RLock()
	defer q.store.mutex.RUnlock()
	
	valueSet := make(map[string]bool)
	
	for _, series := range q.store.series {
		if q.matchesLabels(series.SeriesLabels, matchers) {
			value := series.SeriesLabels.Get(name)
			if value != "" {
				valueSet[value] = true
			}
		}
	}
	
	values := make([]string, 0, len(valueSet))
	for value := range valueSet {
		values = append(values, value)
	}
	
	sort.Strings(values)
	return values, nil, nil
}

func (q *InMemoryQuerier) LabelNames(ctx context.Context, hints *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	q.store.mutex.RLock()
	defer q.store.mutex.RUnlock()
	
	nameSet := make(map[string]bool)
	
	for _, series := range q.store.series {
		if q.matchesLabels(series.SeriesLabels, matchers) {
			for _, label := range series.SeriesLabels {
				nameSet[label.Name] = true
			}
		}
	}
	
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	
	sort.Strings(names)
	return names, nil, nil
}

func (q *InMemoryQuerier) Close() error {
	return nil
}

// SeriesSet interface implementation
func (s *InMemorySeriesSet) Next() bool {
	s.current++
	return s.current < len(s.series)
}

func (s *InMemorySeriesSet) At() storage.Series {
	if s.current < 0 || s.current >= len(s.series) {
		return nil
	}
	return s.series[s.current]
}

func (s *InMemorySeriesSet) Err() error {
	return nil
}

func (s *InMemorySeriesSet) Warnings() annotations.Annotations {
	return nil
}

// TimeSeries implements storage.Series interface
func (ts *TimeSeries) Labels() labels.Labels {
	return ts.SeriesLabels
}

func (ts *TimeSeries) Iterator(it chunkenc.Iterator) chunkenc.Iterator {
	return &InMemorySeriesIterator{
		samples: ts.Samples,
		current: -1,
	}
}

// SeriesIterator interface implementation
func (it *InMemorySeriesIterator) Next() chunkenc.ValueType {
	it.current++
	if it.current >= len(it.samples) {
		return chunkenc.ValNone
	}
	return chunkenc.ValFloat
}

func (it *InMemorySeriesIterator) Seek(t int64) chunkenc.ValueType {
	for i, sample := range it.samples {
		if sample.Timestamp >= t {
			it.current = i
			return chunkenc.ValFloat
		}
	}
	it.current = len(it.samples)
	return chunkenc.ValNone
}

func (it *InMemorySeriesIterator) At() (int64, float64) {
	if it.current < 0 || it.current >= len(it.samples) {
		return 0, 0
	}
	sample := it.samples[it.current]
	return sample.Timestamp, sample.Value
}

func (it *InMemorySeriesIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	return 0, nil
}

func (it *InMemorySeriesIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	return 0, nil
}

func (it *InMemorySeriesIterator) AtT() int64 {
	if it.current < 0 || it.current >= len(it.samples) {
		return 0
	}
	return it.samples[it.current].Timestamp
}

func (it *InMemorySeriesIterator) Err() error {
	return nil
}