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

// User-event subscribe metrics.
//
// The RESUME-vs-FALLBACK ratio and, when it is FALLBACK, the reason, are the
// only way to tell from outside whether delta-resume is doing its job for a
// given deployment: a fleet that is mostly `no_cursor` has clients that are not
// persisting checkpoints, one that is mostly `below_retention_floor` has an
// op-retention window that is too short for how its users work, and
// `corrupt_row` / `park_overflow` are defects. Labelled from the service layer,
// which reads crdt.SubscribeOutcome's Mode and Reason.
//
// Every connect attempt is counted, successful or not, so the series is a
// complete partition of outcomes: a failed connect selected no bootstrap arm
// and lands in mode="invalid", reason="invalid". Without that arm a deployment
// whose ACL resolve started failing would show a DROP in subscribe volume,
// indistinguishable from a drop in traffic.
var (
	UserEventsSubscribeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "leapmux_userevents_subscribe_total",
		Help: "Total /ws/userevents subscribe attempts, by bootstrap mode and the reason for it. A failed connect is mode=invalid.",
	}, []string{"mode", "reason"})

	UserEventsSubscribeDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "leapmux_userevents_subscribe_duration_seconds",
		Help: "Time to resolve the ACL, register, and build the bootstrap frame for a /ws/userevents connect.",
		// A fallback baseline is milliseconds on a large account while a resume
		// is microseconds, so the default buckets (which start at 5ms) put both
		// in the first bucket and answer nothing.
		Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"mode"})
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
