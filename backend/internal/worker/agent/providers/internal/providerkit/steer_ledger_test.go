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

func TestIdentifiedSteerLedgerMatchesEchoesWithAndWithoutBoundTurn(t *testing.T) {
	t.Parallel()
	var ledger IdentifiedSteerLedger
	ledger.Add(agent.SteerResolution{
		InputID: "in-bound", AttemptID: "att-1",
		Receipt: agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: "cmd-1"},
	})
	ledger.Add(agent.SteerResolution{
		InputID: "in-open", AttemptID: "att-2",
		Receipt: agent.NativeInputReceipt{SessionID: "s1", InputID: "cmd-2"},
	})

	// Incomplete echo receipts never match.
	_, ok := ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "", TurnID: "t1", InputID: "cmd-1"})
	assert.False(t, ok)
	_, ok = ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "", InputID: "cmd-1"})
	assert.False(t, ok)
	_, ok = ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: ""})
	assert.False(t, ok)

	// Wrong turn does not match a turn-bound entry.
	_, ok = ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "t2", InputID: "cmd-1"})
	assert.False(t, ok)

	// Matching turn resolves and removes the entry.
	got, ok := ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: "cmd-1"})
	require.True(t, ok)
	assert.Equal(t, "in-bound", got.InputID)
	assert.Equal(t, agent.SteerOutcomeEchoed, got.Outcome)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: "cmd-1"}, got.Receipt)
	_, ok = ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: "cmd-1"})
	assert.False(t, ok)

	// Unbound turn entry matches any turn and records the echoed turn ID.
	got, ok = ledger.TakeEcho(agent.NativeInputReceipt{SessionID: "s1", TurnID: "t9", InputID: "cmd-2"})
	require.True(t, ok)
	assert.Equal(t, "in-open", got.InputID)
	assert.Equal(t, "t9", got.Receipt.TurnID)
}

func TestIdentifiedSteerLedgerTakeUnechoedForgetAndReset(t *testing.T) {
	t.Parallel()
	var ledger IdentifiedSteerLedger
	t1 := ledger.Add(agent.SteerResolution{
		InputID: "a", Receipt: agent.NativeInputReceipt{SessionID: "s1", TurnID: "turn-1", InputID: "c1"},
	})
	t2 := ledger.Add(agent.SteerResolution{
		InputID: "b", Receipt: agent.NativeInputReceipt{SessionID: "s1", TurnID: "turn-2", InputID: "c2"},
	})
	ledger.Add(agent.SteerResolution{
		InputID: "c", Receipt: agent.NativeInputReceipt{SessionID: "s2", TurnID: "turn-1", InputID: "c3"},
	})

	ledger.Forget(t1)
	assert.Empty(t, ledger.TakeUnechoed("s1", "turn-1"))

	remainingS1 := ledger.TakeUnechoed("s1", "")
	require.Len(t, remainingS1, 1)
	assert.Equal(t, "b", remainingS1[0].InputID)
	assert.Empty(t, ledger.TakeUnechoed("s1", ""))

	ledger.Reset()
	assert.Empty(t, ledger.TakeUnechoed("s2", ""))
	t4 := ledger.Add(agent.SteerResolution{
		InputID: "d", Receipt: agent.NativeInputReceipt{SessionID: "s1", TurnID: "turn-3", InputID: "c4"},
	})
	assert.Greater(t, t4, t2)
}

func TestIdentifiedSteerLedgerConcurrentAccess(t *testing.T) {
	t.Parallel()
	var ledger IdentifiedSteerLedger
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipt := agent.NativeInputReceipt{SessionID: "s1", TurnID: "t1", InputID: string(rune('a' + i%26))}
			ticket := ledger.Add(agent.SteerResolution{InputID: receipt.InputID, Receipt: receipt})
			if i%2 == 0 {
				ledger.TakeEcho(receipt)
			} else {
				ledger.Forget(ticket)
			}
		}(i)
	}
	wg.Wait()
	_ = ledger.TakeUnechoed("s1", "")
}
