// Package metrics provides Prometheus instrumentation for LeapMux.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// HTTP metrics.
var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "leapmux_http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "leapmux_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// RPC metrics.
var (
	RPCRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "leapmux_rpc_requests_total",
		Help: "Total number of ConnectRPC requests.",
	}, []string{"service", "method", "code"})

	RPCRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "leapmux_rpc_request_duration_seconds",
		Help:    "ConnectRPC request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method"})
)

// Business metrics.
var (
	ActiveWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "leapmux_active_workers",
		Help: "Number of currently connected workers.",
	})

	ActiveAgents = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "leapmux_active_agents",
		Help: "Number of currently active agents.",
	})

	ActiveTerminals = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "leapmux_active_terminals",
		Help: "Number of currently active terminals.",
	})
)

// WebSocket metrics.
var (
	WSConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "leapmux_ws_connections_active",
		Help: "Number of active WebSocket connections.",
	})

	WSMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "leapmux_ws_messages_total",
		Help: "Total number of WebSocket messages sent.",
	})
)
