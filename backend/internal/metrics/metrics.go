// Package metrics provides Prometheus instrumentation for LeapMux.
package metrics

import (
	"sync"

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

	CaptchaVerificationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "leapmux_captcha_verifications_total",
		Help: "Captcha verification outcomes on protected procedures, by provider (altcha, recaptcha_v3, turnstile, unknown - the label a config-store outage fails closed under).",
	}, []string{"provider", "result"})

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

// Outbound queue metrics.
//
// Each class of connection -- frontend relays, worker links, user-event
// subscribers -- draws its queue memory from its own sendq.Pool, so
// `used / capacity` per pool is the whole answer to "is the Hub close to
// shedding connections for memory, and which kind". Registered from the
// composition root rather than declared here as plain gauges, because the pools
// are values the Hub builds and sendq must stay free of a Prometheus dependency
// (the worker imports it too).
//
// Give-ups are labelled by cause because the causes have opposite fixes: a
// `stall` or `write_timeout` is a slow peer, `over_budget` is a peer whose
// backlog outgrew its own budget, and `pool_pressure` means the DEPLOYMENT is
// undersized -- the connection torn down there was merely the largest holder.
// GiveUpReason.Label() is the only source of these values, so a renamed reason
// cannot silently fork a series.
//
// The pool label uses the SAME vocabulary as leapmux_sendq_pool_* below, and
// that is the point: they describe the same three classes of connection, so a
// dashboard correlating "which pool is under pressure" with "which connections
// are being dropped" must be able to join them. Two spellings for one partition
// -- `writer="channel_relay"` against `pool="relay"` -- silently produced no
// rows for two of the three classes, which are the two most likely to page
// someone.
var SendqGiveUpsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "leapmux_sendq_giveups_total",
	Help: "Outbound queues torn down, by pool and cause.",
}, []string{"pool", "reason"})

// The pool names, and the only source of those label values. A literal at each
// call site is how the two metric families came to disagree; naming them here
// means a rename cannot fork one series from the other.
const (
	PoolRelay      = "relay"
	PoolWorker     = "worker"
	PoolUserEvents = "userevents"
	// PoolWorkerClient is the Worker's own outbound queue to the Hub, which is
	// a PRIVATE per-connection budget rather than one of the Hub's three shared
	// pools -- so it appears in leapmux_sendq_giveups_total and never in
	// leapmux_sendq_pool_*.
	//
	// Deliberately not PoolWorker: that value means the Hub's worker-link pool,
	// and in solo mode both processes are one binary publishing to one
	// registry, so sharing the label would merge the two sides of the same link
	// into a series that could not be read apart.
	PoolWorkerClient = "worker_client"
)

// CountSendqGiveUp records one torn-down outbound queue. `reason` comes from
// sendq.GiveUpReason.Label(), passed as a string so this package stays free of a
// sendq dependency (the worker imports metrics too).
func CountSendqGiveUp(pool, reason string) {
	SendqGiveUpsTotal.WithLabelValues(pool, reason).Inc()
}

// WorkerAdmissionsRefusedTotal counts Workers turned away because the account
// is at max_workers_per_user, by the stage that refused them.
//
// Without it a refused Worker and a machine that never tried look identical
// from outside -- the same reason connections refused at the per-user cap are
// counted.
//
// ONE series with a `stage` label rather than two counters, because
// max_workers_per_user is one cap applied at two points: registering a Worker
// row, and connecting the stream that actually creates the pool member. An
// operator asking "is this cap biting?" wants a single number, and would
// otherwise have to know to sum two. The label is what tells them WHERE it bit,
// which is a different question and the one that decides what an account has to
// do about it: a refused registration means deregister something, a refused
// connect means an existing stream is still holding the slot.
var WorkerAdmissionsRefusedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "leapmux_worker_admissions_refused_total",
	Help: "Workers refused because the account is at its worker cap, by the stage that refused them.",
}, []string{"stage"})

// The stages, and the only source of those label values -- the same rule the
// pool names above follow, for the same reason: a literal at each call site is
// how two spellings of one partition get shipped.
const (
	// WorkerStageRegister: refused while creating the Worker ROW, which is where
	// an operator gets an error naming the key they have to raise.
	WorkerStageRegister = "register"
	// WorkerStageConnect: refused while opening the Connect STREAM, which is
	// where the pool member would have been created. A Worker that is
	// deregistering keeps its stream, so this is the stage that catches a
	// register/deregister cycle the row count no longer sees.
	WorkerStageConnect = "connect"
)

// CountWorkerAdmissionRefused records one Worker refused at its account's cap.
// `stage` must be one of the WorkerStage* constants.
func CountWorkerAdmissionRefused(stage string) {
	WorkerAdmissionsRefusedTotal.WithLabelValues(stage).Inc()
}

// UserEventsFramesDroppedTotal counts frames a /ws/userevents subscriber could
// not take, by the phase it was in and which bound refused it.
//
// The two labels carry the whole story and are not interchangeable. `phase`
// says what the drop COST: park-phase drops fall back to a snapshot with the
// connection intact, live-phase drops disconnect the subscriber.
//
// `bound` says what to DO about it, and its three values call for three
// different actions:
//
//   - `frames`: the subscriber is behind in frames the client has not applied.
//     A slow client, not a deployment problem.
//   - `bytes`: the shared budget was full at that moment. The deployment's to
//     fix, and it correlates -- leapmux_sendq_pool_used_bytes{pool="userevents"}
//     is near capacity at the same instant.
//   - `capacity`: the frame is larger than the WHOLE budget, so it is refused at
//     every occupancy, including an empty pool. No correlation with used_bytes
//     to look for, and nothing to wait for: only raising
//     userevents_queue_memory_budget clears it.
//
// The last two are the reason `bytes` alone was not enough. Read as one value,
// a bootstrap frame that can never fit looks like transient pressure, and an
// operator watching occupancy for a spike that never comes concludes the metric
// is lying.
//
// Without this the byte bounds are invisible from outside: a subscriber refused
// on bytes and one refused on frames both surface as a reconnect.
var UserEventsFramesDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "leapmux_userevents_frames_dropped_total",
	Help: "User-event frames a subscriber could not take, by delivery phase and the bound that refused them.",
}, []string{"phase", "bound"})

// ConnectionsRefusedTotal counts long-lived connections turned away after they
// authenticated but before they were served, by reason.
//
// The queue pools bound the BYTES a class of connection may hold; this bounds
// how many one user may open, which is what stops the pools' per-connection
// guarantees from multiplying by a number nothing limits. Without a counter the
// cap is invisible from outside -- a refused connection and a client that never
// dialled look identical -- so an operator whose users start hitting it would
// see only unexplained reconnects.
//
// auth.LeaseOutcome.Label() is the only source of these values, so a renamed
// outcome cannot silently fork a series.
var ConnectionsRefusedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "leapmux_connections_refused_total",
	Help: "Long-lived connections refused after authenticating, by reason.",
}, []string{"reason"})

// PoolStats is the read-only view of a sendq.Pool the collector needs.
// Declared here rather than taking *sendq.Pool so this package does not depend
// on sendq purely to name a type.
type PoolStats interface {
	Capacity() int64
	Used() int64
	Members() int64
	Evictions() int64
	Overcommits() int64
}

// sendqPoolCollector publishes the Hub's outbound queue budgets.
//
// A collector reading each pool on scrape, rather than gauges the queues
// update: the pools' counters are already atomics maintained on the enqueue
// path, and scraping should read them instead of adding a second write to every
// frame.
//
// Pools arrive after this package is initialised -- the Hub builds them from
// config -- so the collector is registered once at init with an empty table and
// SetSendqPool fills it. Registering per pool instead would panic the second
// time a process built a Hub, which is every test binary that exercises more
// than one.
type sendqPoolCollector struct {
	mu    sync.Mutex
	pools map[string]PoolStats

	// stats is the single list Describe and Collect both walk. Spelling the five
	// out four times over -- field, desc, Describe, Collect -- meant a sixth was
	// four synchronised edits, and a Collect entry with no matching Describe
	// breaks /metrics at scrape time rather than at compile time.
	stats []poolStat
}

// poolStat is one published number: its descriptor, its Prometheus kind, and
// how to read it off a pool.
type poolStat struct {
	desc *prometheus.Desc
	kind prometheus.ValueType
	read func(PoolStats) float64
}

var sendqPools = func() *sendqPoolCollector {
	labels := []string{"pool"}
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}
	c := &sendqPoolCollector{
		pools: map[string]PoolStats{},
		stats: []poolStat{
			{
				desc: desc("leapmux_sendq_pool_capacity_bytes",
					"Configured outbound queue memory shared by every member of this pool."),
				kind: prometheus.GaugeValue,
				read: func(p PoolStats) float64 { return float64(p.Capacity()) },
			},
			{
				desc: desc("leapmux_sendq_pool_used_bytes",
					"Outbound queue memory currently resident across this pool. Each buffer "+
						"counts once however many members hold it."),
				kind: prometheus.GaugeValue,
				read: func(p PoolStats) float64 { return float64(p.Used()) },
			},
			{
				desc: desc("leapmux_sendq_pool_members",
					"Connections currently drawing from this pool."),
				kind: prometheus.GaugeValue,
				read: func(p PoolStats) float64 { return float64(p.Members()) },
			},
			{
				desc: desc("leapmux_sendq_pool_evictions_total",
					"Connections torn down to reclaim this pool's memory."),
				kind: prometheus.CounterValue,
				read: func(p PoolStats) float64 { return float64(p.Evictions()) },
			},
			{
				desc: desc("leapmux_sendq_pool_overcommits_total",
					"Times a guaranteed per-member floor was granted without room for it. "+
						"Sustained growth means this pool is too small for its connection count."),
				kind: prometheus.CounterValue,
				read: func(p PoolStats) float64 { return float64(p.Overcommits()) },
			},
		},
	}
	prometheus.MustRegister(c)
	return c
}()

// SetSendqPool publishes p's counters at /metrics under the given pool name,
// replacing any pool registered under that name before. Replacement rather than
// accumulation is what keeps a test binary that builds several Hubs reporting
// the live one instead of a pile of dead ones.
func SetSendqPool(name string, p PoolStats) {
	sendqPools.mu.Lock()
	defer sendqPools.mu.Unlock()
	sendqPools.pools[name] = p
}

func (c *sendqPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, s := range c.stats {
		ch <- s.desc
	}
}

func (c *sendqPoolCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	snapshot := make(map[string]PoolStats, len(c.pools))
	for name, p := range c.pools {
		snapshot[name] = p
	}
	c.mu.Unlock()

	// Nothing is emitted before a Hub is built (or in a worker process, which
	// has no shared budget). Emitting zeroes would read as empty pools rather
	// than as no pools.
	for name, p := range snapshot {
		for _, s := range c.stats {
			ch <- prometheus.MustNewConstMetric(s.desc, s.kind, s.read(p), name)
		}
	}
}

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
