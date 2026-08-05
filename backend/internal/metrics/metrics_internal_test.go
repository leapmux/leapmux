package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePool is a fixed reading of a sendq.Pool.
type fakePool struct {
	capacity, used, members, evictions, overcommits int64
}

func (p fakePool) Capacity() int64    { return p.capacity }
func (p fakePool) Used() int64        { return p.used }
func (p fakePool) Members() int64     { return p.members }
func (p fakePool) Evictions() int64   { return p.evictions }
func (p fakePool) Overcommits() int64 { return p.overcommits }

// resetSendqPools empties the collector's table before and after a test so one
// test's pools cannot leak into another's scrape. The collector itself is
// registered once at init and is deliberately never unregistered -- that single
// registration is the property under test.
func resetSendqPools(t *testing.T) {
	t.Helper()
	clear := func() {
		sendqPools.mu.Lock()
		defer sendqPools.mu.Unlock()
		sendqPools.pools = map[string]PoolStats{}
	}
	clear()
	t.Cleanup(clear)
}

// scrape collects the pool collector into a private registry and returns
// name -> pool-label -> value, plus the metric TYPE per name. A private
// registry rather than the default one keeps the assertions from depending on
// whatever else the binary has registered.
func scrape(t *testing.T) (map[string]map[string]float64, map[string]dto.MetricType) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(sendqPools))
	families, err := reg.Gather()
	require.NoError(t, err)

	values := map[string]map[string]float64{}
	types := map[string]dto.MetricType{}
	for _, f := range families {
		types[f.GetName()] = f.GetType()
		byPool := map[string]float64{}
		for _, m := range f.GetMetric() {
			var pool string
			for _, l := range m.GetLabel() {
				if l.GetName() == "pool" {
					pool = l.GetValue()
				}
			}
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				byPool[pool] = m.GetCounter().GetValue()
			default:
				byPool[pool] = m.GetGauge().GetValue()
			}
		}
		values[f.GetName()] = byPool
	}
	return values, types
}

// TestSetSendqPoolPublishesEachPoolSeparately pins that the Hub's two budgets
// are distinguishable at /metrics. Without the label an operator could watch the
// Hub shed connections with no way to tell WHICH budget was binding -- and which
// budget is binding is the only actionable thing the series carries.
func TestSetSendqPoolPublishesEachPoolSeparately(t *testing.T) {
	resetSendqPools(t)

	SetSendqPool("relay", fakePool{capacity: 800, used: 300, members: 7, evictions: 2, overcommits: 1})
	SetSendqPool("worker", fakePool{capacity: 400, used: 40, members: 3})

	values, types := scrape(t)

	assert.Equal(t, map[string]float64{"relay": 800, "worker": 400}, values["leapmux_sendq_pool_capacity_bytes"])
	assert.Equal(t, map[string]float64{"relay": 300, "worker": 40}, values["leapmux_sendq_pool_used_bytes"])
	assert.Equal(t, map[string]float64{"relay": 7, "worker": 3}, values["leapmux_sendq_pool_members"])
	assert.Equal(t, map[string]float64{"relay": 2, "worker": 0}, values["leapmux_sendq_pool_evictions_total"])
	assert.Equal(t, map[string]float64{"relay": 1, "worker": 0}, values["leapmux_sendq_pool_overcommits_total"])

	// The reclaim counters must be typed as counters, or a rate() over them is
	// meaningless -- and a monotonic value published as a gauge is exactly the
	// mistake a `_total` suffix invites.
	assert.Equal(t, dto.MetricType_COUNTER, types["leapmux_sendq_pool_evictions_total"])
	assert.Equal(t, dto.MetricType_COUNTER, types["leapmux_sendq_pool_overcommits_total"])
	assert.Equal(t, dto.MetricType_GAUGE, types["leapmux_sendq_pool_used_bytes"])
}

// TestSetSendqPoolIsIdempotent pins the property a per-pool promauto
// registration violates: publishing a pool twice must replace it, not panic
// with "duplicate metrics collector registration attempted" and not leave two
// series behind.
//
// A regression pin rather than a hypothetical -- every test binary that builds
// more than one Hub does exactly this, and the first cut of this code crashed on
// it.
func TestSetSendqPoolIsIdempotent(t *testing.T) {
	resetSendqPools(t)

	SetSendqPool("relay", fakePool{capacity: 100, used: 10})
	require.NotPanics(t, func() {
		SetSendqPool("relay", fakePool{capacity: 999, used: 42})
	})

	values, _ := scrape(t)
	assert.Equal(t, map[string]float64{"relay": 42}, values["leapmux_sendq_pool_used_bytes"],
		"the live pool must win, not the dead one registered first, and no second series may be left behind")
}

// TestSendqPoolCollectorReadsThePoolOnEveryScrape pins that the gauges follow a
// live pool rather than snapshotting it at registration. The counters are
// atomics the enqueue path already maintains; the whole reason this is a
// collector and not a set of gauges is that scraping should read them.
func TestSendqPoolCollectorReadsThePoolOnEveryScrape(t *testing.T) {
	resetSendqPools(t)

	pool := &mutablePool{}
	SetSendqPool("relay", pool)

	pool.used = 11
	first, _ := scrape(t)
	assert.InDelta(t, 11, first["leapmux_sendq_pool_used_bytes"]["relay"], 0)

	pool.used = 22
	second, _ := scrape(t)
	assert.InDelta(t, 22, second["leapmux_sendq_pool_used_bytes"]["relay"], 0)
}

type mutablePool struct{ used int64 }

func (p *mutablePool) Capacity() int64    { return 100 }
func (p *mutablePool) Used() int64        { return p.used }
func (p *mutablePool) Members() int64     { return 1 }
func (p *mutablePool) Evictions() int64   { return 0 }
func (p *mutablePool) Overcommits() int64 { return 0 }

// TestSendqPoolCollectorEmitsNothingWithoutAPool pins that a scrape before any
// Hub is built -- or in a worker process, which has no shared budget -- reports
// NO pool rather than an empty one. Zeroes would read as "the budget is idle",
// which is a different and wrong statement.
func TestSendqPoolCollectorEmitsNothingWithoutAPool(t *testing.T) {
	resetSendqPools(t)

	values, _ := scrape(t)
	for name, byPool := range values {
		assert.Empty(t, byPool, "%s must publish no series while no pool is registered", name)
	}
}
