package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/metrics"
)

func getCounterValue(t *testing.T, counter *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	c, err := counter.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	_ = c.(prometheus.Metric).Write(m)
	return m.GetCounter().GetValue()
}

func getGaugeValue(t *testing.T, gauge prometheus.Gauge) float64 {
	t.Helper()
	m := &dto.Metric{}
	_ = gauge.(prometheus.Metric).Write(m)
	return m.GetGauge().GetValue()
}

func getHistogramCount(t *testing.T, hist *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	o, err := hist.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	_ = o.(prometheus.Metric).Write(m)
	return m.GetHistogram().GetSampleCount()
}

// --- ParseProcedure tests ---

func TestParseProcedure(t *testing.T) {
	tests := []struct {
		procedure string
		wantSvc   string
		wantMeth  string
	}{
		{"/leapmux.v1.FooService/BarMethod", "FooService", "BarMethod"},
		{"/leapmux.v1.WorkspaceService/CreateWorkspace", "WorkspaceService", "CreateWorkspace"},
		{"/simple.Service/Method", "Service", "Method"},
		{"invalid", "unknown", "unknown"},
		{"", "unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.procedure, func(t *testing.T) {
			svc, method := metrics.ParseProcedure(tt.procedure)
			assert.Equal(t, tt.wantSvc, svc)
			assert.Equal(t, tt.wantMeth, method)
		})
	}
}

// --- HTTP Middleware tests ---

func TestHTTPMiddleware_RecordsRequestMetrics(t *testing.T) {
	handler := metrics.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	beforeCount := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "200")
	beforeHistCount := getHistogramCount(t, metrics.HTTPRequestDuration, "GET", "/static")

	resp, err := http.Get(server.URL + "/some/asset.js")
	require.NoError(t, err)
	_ = resp.Body.Close()

	afterCount := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "200")
	afterHistCount := getHistogramCount(t, metrics.HTTPRequestDuration, "GET", "/static")

	assert.Equal(t, float64(1), afterCount-beforeCount)
	assert.Equal(t, uint64(1), afterHistCount-beforeHistCount)
}

func TestHTTPMiddleware_NormalizesPaths(t *testing.T) {
	handler := metrics.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	// RPC path should be kept as-is.
	beforeRPC := getCounterValue(t, metrics.HTTPRequestsTotal, "POST", "/leapmux.v1.AuthService/Login", "200")
	req, _ := http.NewRequest("POST", server.URL+"/leapmux.v1.AuthService/Login", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	afterRPC := getCounterValue(t, metrics.HTTPRequestsTotal, "POST", "/leapmux.v1.AuthService/Login", "200")
	assert.Equal(t, float64(1), afterRPC-beforeRPC)

	// /metrics path should be kept as-is.
	beforeMetrics := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/metrics", "200")
	resp, err = http.Get(server.URL + "/metrics")
	require.NoError(t, err)
	_ = resp.Body.Close()
	afterMetrics := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/metrics", "200")
	assert.Equal(t, float64(1), afterMetrics-beforeMetrics)

	// Static asset paths should be grouped as /static.
	beforeStatic := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "200")
	resp, err = http.Get(server.URL + "/assets/bundle.js")
	require.NoError(t, err)
	_ = resp.Body.Close()
	afterStatic := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "200")
	assert.Equal(t, float64(1), afterStatic-beforeStatic)
}

func TestHTTPMiddleware_Records404(t *testing.T) {
	handler := metrics.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	beforeCount := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "404")

	resp, err := http.Get(server.URL + "/nonexistent")
	require.NoError(t, err)
	_ = resp.Body.Close()

	afterCount := getCounterValue(t, metrics.HTTPRequestsTotal, "GET", "/static", "404")
	assert.Equal(t, float64(1), afterCount-beforeCount)
}

// --- Business gauge tests ---

func TestActiveWorkersGauge(t *testing.T) {
	before := getGaugeValue(t, metrics.ActiveWorkers)
	metrics.ActiveWorkers.Inc()
	after := getGaugeValue(t, metrics.ActiveWorkers)
	assert.Equal(t, float64(1), after-before)

	metrics.ActiveWorkers.Dec()
	afterDec := getGaugeValue(t, metrics.ActiveWorkers)
	assert.Equal(t, before, afterDec)
}

func TestActiveAgentsGauge(t *testing.T) {
	before := getGaugeValue(t, metrics.ActiveAgents)
	metrics.ActiveAgents.Inc()
	after := getGaugeValue(t, metrics.ActiveAgents)
	assert.Equal(t, float64(1), after-before)

	metrics.ActiveAgents.Dec()
	afterDec := getGaugeValue(t, metrics.ActiveAgents)
	assert.Equal(t, before, afterDec)
}

func TestActiveTerminalsGauge(t *testing.T) {
	before := getGaugeValue(t, metrics.ActiveTerminals)
	metrics.ActiveTerminals.Inc()
	after := getGaugeValue(t, metrics.ActiveTerminals)
	assert.Equal(t, float64(1), after-before)

	metrics.ActiveTerminals.Dec()
	afterDec := getGaugeValue(t, metrics.ActiveTerminals)
	assert.Equal(t, before, afterDec)
}

// --- Registry test ---

func TestMetricsRegistered(t *testing.T) {
	count, err := testutil.GatherAndCount(prometheus.DefaultGatherer)
	require.NoError(t, err)
	assert.Greater(t, count, 0, "should have registered metrics")
}
