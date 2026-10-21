package providerkit

import (
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSteerLedgerSettlesEchoesInOrder(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a, b := ledger.Add("a"), ledger.Add("b")
	ledger.Echo(a)
	ledger.Echo(b)
	ledger.Settle(sink)
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
}

func TestSteerLedgerHoldsALaterEchoUntilTheHeadResolves(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a, b := ledger.Add("a"), ledger.Add("b")
	ledger.Echo(b)
	ledger.Settle(sink)
	assert.Empty(t, sink.SteerEchoReports())
	assert.Equal(t, []SteerTicket{a}, ledger.Pending())
	ledger.Echo(a)
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
}

func TestSteerLedgerReportsADropAfterTheEchoesBeforeIt(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a, b, c := ledger.Add("a"), ledger.Add("b"), ledger.Add("c")
	ledger.Echo(a)
	ledger.Drop(b)
	ledger.Echo(c)
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}, {Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerLedgerForgetReportsNothing(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a, b := ledger.Add("a"), ledger.Add("b")
	ledger.Echo(b)
	ledger.Settle(sink)
	ledger.Forget(a)
	ledger.Forget(a)
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerLedgerEchoKeyMatchesTheOldestPendingEntry(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	a, b := ledger.Add("same"), ledger.Add("same")
	assert.False(t, ledger.EchoKey("unknown"))
	assert.True(t, ledger.EchoKey("same"))
	assert.Equal(t, []SteerTicket{b}, ledger.Pending())
	ledger.Drop(a)
	assert.True(t, ledger.EchoKey("same"))
	assert.False(t, ledger.EchoKey("same"))
	sink := &agenttest.Sink{}
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 2}}, sink.SteerEchoReports())
}

func TestSteerLedgerEchoIDUsesTheNativeID(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	a, b := ledger.Add(""), ledger.Add("")
	assert.False(t, ledger.EchoID(""))
	assert.Empty(t, ledger.ID(a))
	ledger.SetID(a, "native-a")
	ledger.SetID(b, "native-b")
	ledger.SetID(0, "unknown")
	assert.Equal(t, "native-a", ledger.ID(a))
	assert.Empty(t, ledger.ID(0))
	assert.False(t, ledger.EchoID("missing"))
	assert.True(t, ledger.EchoID("native-b"))
	assert.False(t, ledger.EchoID("native-b"))
	assert.Equal(t, []SteerTicket{a}, ledger.Pending())
}

func TestSteerLedgerEchoOldestCountsOnlyPendingEntries(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	a, b, c := ledger.Add("a"), ledger.Add("b"), ledger.Add("c")
	ledger.Echo(b)
	assert.Zero(t, ledger.EchoOldest(0))
	assert.Zero(t, ledger.EchoOldest(-1))
	assert.Equal(t, []SteerTicket{a, c}, ledger.Pending())
	assert.Equal(t, 1, ledger.EchoOldest(1))
	assert.Equal(t, []SteerTicket{c}, ledger.Pending())
	assert.Equal(t, 1, ledger.EchoOldest(100))
	assert.Zero(t, ledger.EchoOldest(100))
}

func TestSteerLedgerDropAllDropsOnlyPendingEntries(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a := ledger.Add("a")
	ledger.Add("b")
	ledger.Add("c")
	ledger.Echo(a)
	assert.Equal(t, 2, ledger.DropAll())
	assert.Zero(t, ledger.DropAll())
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 2}}, sink.SteerEchoReports())
}

func TestSteerLedgerResetForgetsEveryEntry(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	a, b := ledger.Add("a"), ledger.Add("b")
	ledger.Echo(b)
	ledger.Reset()
	ledger.Settle(sink)
	assert.Empty(t, ledger.Pending())
	assert.Empty(t, sink.SteerEchoReports())
	c := ledger.Add("c")
	assert.Greater(t, c, b)
	ledger.Echo(a)
	assert.Equal(t, []SteerTicket{c}, ledger.Pending())
}

type ledgerCallbackSink struct {
	*agenttest.Sink
	onEcho func()
}

func (s ledgerCallbackSink) ReportSteersEchoed(count int) {
	s.Sink.ReportSteersEchoed(count)
	s.onEcho()
}

func TestSteerLedgerSettleRunsOutsideTheLock(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	ledger.Echo(ledger.Add("a"))
	sink := ledgerCallbackSink{Sink: &agenttest.Sink{}}
	sink.onEcho = func() {
		assert.Empty(t, ledger.Pending())
		ledger.Drop(ledger.Add("b"))
		ledger.Settle(sink)
	}
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerLedgerConcurrentSettlesKeepReportOrder(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	a, b := ledger.Add("a"), ledger.Add("b")
	ledger.Echo(a)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	sink := ledgerCallbackSink{Sink: &agenttest.Sink{}, onEcho: func() {
		close(entered)
		<-release
	}}
	go func() { ledger.Settle(sink); close(done) }()
	<-entered
	ledger.Drop(b)
	ledger.Settle(sink)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	close(release)
	<-done
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}, {Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerLedgerConcurrentResolutionReportsEverySteerOnce(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	var callers sync.WaitGroup
	for range 100 {
		callers.Go(func() {
			ticket := ledger.Add("same")
			ledger.SetID(ticket, "native")
			ledger.Echo(ticket)
			ledger.Settle(sink)
		})
	}
	callers.Wait()
	ledger.Settle(sink)
	total := 0
	for _, report := range sink.SteerEchoReports() {
		require.Equal(t, agent.SteerOutcomeEchoed, report.Outcome)
		total += report.Count
	}
	assert.Equal(t, 100, total)
	assert.Empty(t, ledger.Pending())
}

func TestSteerLedgerDropIDMatchesOnlyTheOldestPendingNativeID(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	first, second := ledger.Add("same"), ledger.Add("same")
	ledger.SetID(first, "native")
	ledger.SetID(second, "native")
	assert.False(t, ledger.DropID(""))
	assert.False(t, ledger.DropID("absent"))
	assert.True(t, ledger.DropID("native"))
	assert.Equal(t, []SteerTicket{second}, ledger.Pending())
	assert.True(t, ledger.DropID("native"))
	assert.False(t, ledger.DropID("native"))
}

func TestSteerLedgerPendingEntriesPreserveKeysAndIDs(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	first := ledger.Add("first prompt")
	second := ledger.Add("second prompt")
	empty := ledger.Add("")
	ledger.SetID(first, "native-first")
	ledger.Echo(second)
	entries := ledger.PendingEntries()
	require.Equal(t, []PendingSteer{{Ticket: first, Key: "first prompt", ID: "native-first"}, {Ticket: empty}}, entries)
	entries[0].Key = "changed"
	ledger.SetID(first, "new-id")
	assert.Equal(t, "native-first", entries[0].ID)
	assert.True(t, ledger.EchoKey("first prompt"))
	ledger.Forget(empty)
	assert.Empty(t, ledger.PendingEntries())
}

func TestSteerLedgerSettlesUncertaintyInOrder(t *testing.T) {
	t.Parallel()
	var ledger SteerLedger
	sink := &agenttest.Sink{}
	uncertain, ok := any(&ledger).(interface{ Uncertain(SteerTicket) })
	require.True(t, ok, "the ledger must retain the order of uncertain deliveries")
	first, second, third, fourth := ledger.Add("first"), ledger.Add("second"), ledger.Add("third"), ledger.Add("fourth")
	uncertain.Uncertain(second)
	ledger.Echo(third)
	ledger.Drop(fourth)
	ledger.Settle(sink)
	assert.Empty(t, sink.SteerEchoReports())
	ledger.Echo(first)
	ledger.Settle(sink)
	assert.Equal(t, []string{"steer-echoed:1", "steer-uncertain:1", "steer-echoed:1", "steer-dropped:1"}, sink.TurnLifecycle())
	assert.Empty(t, ledger.Pending())
}
