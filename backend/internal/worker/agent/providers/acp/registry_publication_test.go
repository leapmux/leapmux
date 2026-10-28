package acp

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registryPublicationEvent struct {
	operation string
	key       string
	committed bool
}

type registryPublicationSink struct {
	*agenttest.ControlSink
	mu      sync.Mutex
	blocked string
	events  []registryPublicationEvent
	hook    func()
}

type registryPublicationNoQueueSink struct{ *agenttest.ControlSink }

func (*registryPublicationNoQueueSink) OutputQueue() *agent.OutputQueue { return nil }

func (sink *registryPublicationSink) apply(operation, key string, run func() error) error {
	sink.mu.Lock()
	index := len(sink.events)
	sink.events = append(sink.events, registryPublicationEvent{operation: operation, key: key})
	blocked := sink.blocked == operation
	hook := sink.hook
	if operation == "upsert" {
		sink.hook = nil
	} else {
		hook = nil
	}
	sink.mu.Unlock()
	if hook != nil {
		hook()
	}
	if blocked {
		return errors.New("the registry storage is unavailable")
	}
	err := run()
	sink.mu.Lock()
	sink.events[index].committed = err == nil
	sink.mu.Unlock()
	return err
}

func (sink *registryPublicationSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	return sink.apply("upsert", task.RowKey, func() error { return sink.ControlSink.UpsertBackgroundTask(task) })
}

func (sink *registryPublicationSink) RenameBackgroundTask(from, to string) error {
	return sink.apply("rename", to, func() error { return sink.ControlSink.RenameBackgroundTask(from, to) })
}

func (sink *registryPublicationSink) CloseBackgroundTask(key string, status bgtask.Status) error {
	return sink.apply("close", key, func() error { return sink.ControlSink.CloseBackgroundTask(key, status) })
}

func (sink *registryPublicationSink) CleanupChildAgent(childID string) {
	_ = sink.apply("cleanup", childID, func() error {
		sink.ControlSink.CleanupChildAgent(childID)
		return nil
	})
}

func (sink *registryPublicationSink) snapshot() []registryPublicationEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]registryPublicationEvent(nil), sink.events...)
}

func (sink *registryPublicationSink) allow() {
	sink.mu.Lock()
	sink.blocked = ""
	sink.mu.Unlock()
}

func newRegistryPublicationBase(t *testing.T) (*Base, *registryPublicationSink, *agent.OutputQueue) {
	t.Helper()
	sink := &registryPublicationSink{ControlSink: &agenttest.ControlSink{}}
	services := agent.NewProviderServices(sink)
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	b := &Base{sink: agent.NewOrderedProviderServices(queue, services), sessionID: "session-1"}
	return b, sink, queue
}

func TestACPRegistryPublicationRetainsImmutableStages(t *testing.T) {
	for _, operation := range []string{"rename", "upsert", "close"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			require.NoError(t, sink.ControlSink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "temporary", Title: "Original title", Status: bgtask.StatusRunning}))
			b.openRows.opened("temporary")
			sink.blocked = operation
			obs := &SubagentObservation{
				RegistryOnly: true, RowKey: "native", RenameFrom: "temporary", Kind: bgtask.KindWorkflow,
				Title: "Native final title", Activity: "Native final activity", GroupKey: "group", GroupLabel: "Native group",
				Status: bgtask.StatusSucceeded, CloseRow: true,
			}
			require.NoError(t, b.ApplyRegistryObservation(obs))
			require.Empty(t, sink.snapshot(), "admission must not call registry observers")
			obs.RowKey, obs.RenameFrom, obs.Title, obs.Activity = "mutated", "foreign", "Mutated title", "Mutated activity"
			obs.Status = bgtask.StatusFailed
			require.Error(t, queue.Flush())
			require.True(t, queue.HasPending())
			if operation == "rename" {
				_, found := sink.BackgroundTask("temporary")
				assert.True(t, found, "a refused rename preserves its source until retry")
				_, found = sink.BackgroundTask("native")
				assert.False(t, found, "later stages must not create the destination before its rename succeeds")
			}
			sink.allow()
			require.NoError(t, queue.Flush())
			require.False(t, queue.HasPending())
			row, found := sink.BackgroundTask("native")
			require.True(t, found)
			assert.Equal(t, "Native final title", row.Title)
			assert.Equal(t, "Native final activity", row.ActiveForm)
			assert.Equal(t, "Native group", row.GroupLabel)
			assert.Equal(t, bgtask.KindWorkflow, row.Kind)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			_, found = sink.BackgroundTask("mutated")
			assert.False(t, found)
			var committed []registryPublicationEvent
			for _, event := range sink.snapshot() {
				if event.committed {
					committed = append(committed, event)
				}
			}
			assert.Equal(t, []registryPublicationEvent{
				{operation: "rename", key: "native", committed: true},
				{operation: "upsert", key: "native", committed: true},
				{operation: "close", key: "native", committed: true},
			}, committed, "a retry resumes at its refused primitive")
			assert.Empty(t, b.openRows.takeAll(), "the final admitted batch needs no retirement close")
		})
	}
}

func TestACPRegistryPublicationRejectsInvalidAdmission(t *testing.T) {
	cases := map[string]func(*SubagentObservation){
		"empty key":                 func(obs *SubagentObservation) { obs.RowKey = "" },
		"blank key":                 func(obs *SubagentObservation) { obs.RowKey = " \t" },
		"blank rename":              func(obs *SubagentObservation) { obs.RenameFrom = "\n" },
		"negative mode":             func(obs *SubagentObservation) { obs.Mode = -1 },
		"unknown mode":              func(obs *SubagentObservation) { obs.Mode = 100 },
		"close-only without close":  func(obs *SubagentObservation) { obs.Mode = ModeCloseOnly },
		"close with running status": func(obs *SubagentObservation) { obs.CloseRow = true },
		"child key":                 func(obs *SubagentObservation) { obs.ChildAgentKey = "child" },
		"child spawn":               func(obs *SubagentObservation) { obs.ChildSpawnSpanID = "spawn" },
		"child session":             func(obs *SubagentObservation) { obs.ChildAgentSessionID = "native-child" },
		"prompt":                    func(obs *SubagentObservation) { obs.Prompt = "A child instruction." },
		"empty child payload":       func(obs *SubagentObservation) { obs.ChildTranscriptPayload = []byte{} },
		"empty child update":        func(obs *SubagentObservation) { obs.ChildUpdate = json.RawMessage{} },
		"child flush":               func(obs *SubagentObservation) { obs.ChildUpdateFlush = true },
		"report identity":           func(obs *SubagentObservation) { obs.ReportID = "report" },
		"report label":              func(obs *SubagentObservation) { obs.Report.Label = "A report" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			obs := &SubagentObservation{RegistryOnly: true, RowKey: "row", Status: bgtask.StatusRunning}
			change(obs)
			require.Error(t, b.ApplyRegistryObservation(obs))
			assert.False(t, queue.HasPending())
			assert.Empty(t, sink.snapshot())
			assert.Empty(t, b.openRows.takeAll())
		})
	}
	t.Run("nil observation", func(t *testing.T) {
		b, _, queue := newRegistryPublicationBase(t)
		require.Error(t, b.ApplyRegistryObservation(nil))
		assert.False(t, queue.HasPending())
	})
	t.Run("absent owner", func(t *testing.T) {
		b, _, queue := newRegistryPublicationBase(t)
		b.sessionID = ""
		require.Error(t, b.ApplyRegistryObservation(&SubagentObservation{RowKey: "row", Status: bgtask.StatusRunning}))
		assert.False(t, queue.HasPending())
	})
	t.Run("absent queue", func(t *testing.T) {
		b, sink, queue := newRegistryPublicationBase(t)
		b.sink = agent.NewProviderServices(&registryPublicationNoQueueSink{ControlSink: sink.ControlSink})
		require.Error(t, b.ApplyRegistryObservation(&SubagentObservation{RowKey: "row", Status: bgtask.StatusRunning}))
		assert.False(t, queue.HasPending())
		assert.Empty(t, sink.snapshot())
		assert.Empty(t, b.openRows.takeAll())
	})
}

func TestACPRegistryPublicationRejectsIncidentalFlushUnderSourceLocks(t *testing.T) {
	for _, source := range []string{"session dispatch", "session write lease", "session read lease", "turn state", "protocol state", "child feed"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			conversation := b.main()
			b.sessionUpdates.mu.Lock()
			if source == "child feed" {
				conversation.publicationMutex = &sync.Mutex{}
			}
			require.NoError(t, conversation.applyRegistryObservation(&SubagentObservation{RegistryOnly: true, RowKey: "row", Status: bgtask.StatusRunning}))
			release := b.sessionUpdates.mu.Unlock
			if source != "session dispatch" {
				b.sessionUpdates.mu.Unlock()
				switch source {
				case "session write lease":
					b.sessionMu.Lock()
					release = b.sessionMu.Unlock
				case "session read lease":
					b.sessionMu.RLock()
					release = b.sessionMu.RUnlock
				case "turn state":
					b.turnMu.Lock()
					release = b.turnMu.Unlock
				case "protocol state":
					b.Mu.Lock()
					release = b.Mu.Unlock
				case "child feed":
					conversation.publicationMutex.Lock()
					release = conversation.publicationMutex.Unlock
				}
			}
			err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"ordinary-native-row"}`)}, agent.SpanInfo{})
			events := sink.snapshot()
			release()
			require.True(t, agent.OutputRetained(err), "the ordinary transcript flush must retain the blocked registry primitive")
			require.Error(t, err)
			require.Empty(t, events, "no registry observer may run while the source owns a publication lock")
			reentered := false
			sink.hook = func() {
				reentered = b.RouteSessionNotification(SessionNotification{
					ResolveSession: func(json.RawMessage) (string, bool) { return "session-1", true },
					Dispatch:       func(string, json.RawMessage) {},
				})
			}
			require.NoError(t, queue.Flush())
			assert.True(t, reentered, "the registry observer can reenter native dispatch after source release")
			assert.False(t, queue.HasPending())
			assert.Equal(t, 1, sink.MessageCount(), "the ordinary row stays ordered after the retained registry write")
		})
	}
}

func TestACPRegistryPublicationAutomaticRetryRespectsTheSessionLease(t *testing.T) {
	b, sink, queue := newRegistryPublicationBase(t)
	clock := testutil.NewQuartzMock(t)
	created := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
	stopped := clock.Trap().TimerStop(agent.OutputRetryTimerTag)
	closeCreated, closeStopped := sync.OnceFunc(created.Close), sync.OnceFunc(stopped.Close)
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(func() { closeCreated(); closeStopped(); queue.StopRetries() })
	require.NoError(t, b.ApplyRegistryObservation(&SubagentObservation{RegistryOnly: true, RowKey: "row", Status: bgtask.StatusRunning}))
	b.sessionMu.RLock()
	require.ErrorIs(t, queue.Flush(), errRegistryPublicationLocked)
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, created)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	delay = testutil.WaitForTimer(t, ctx, created)
	events := sink.snapshot()
	b.sessionMu.RUnlock()
	closeCreated()
	require.Empty(t, events, "an automatic retry must not publish through an active session lease")
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	queue.StopRetries()
	assert.False(t, queue.HasPending())
	assert.Len(t, sink.snapshot(), 1)
}

func TestACPRegistryRetirementFollowsAcceptedWritesAndCapturesTheOriginalChild(t *testing.T) {
	b, sink, queue := newRegistryPublicationBase(t)
	b.rememberChildAgent("row", "original-child", b.sink)
	sink.blocked = "upsert"
	require.NoError(t, b.ApplyRegistryObservation(&SubagentObservation{RegistryOnly: true, RowKey: "row", Status: bgtask.StatusRunning}))
	b.retireSession("session-1")
	require.True(t, queue.HasPending())
	b.rememberChildAgent("row", "replacement-child", b.sink)
	sink.allow()
	require.NoError(t, queue.Flush())
	row, found := sink.BackgroundTask("row")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	var committed []registryPublicationEvent
	for _, event := range sink.snapshot() {
		if event.committed {
			committed = append(committed, event)
		}
	}
	assert.Equal(t, []registryPublicationEvent{
		{operation: "upsert", key: "row", committed: true},
		{operation: "close", key: "row", committed: true},
		{operation: "cleanup", key: "original-child", committed: true},
	}, committed)
	b.children.mu.Lock()
	assert.Equal(t, "replacement-child", b.children.agents["row"].id)
	b.children.mu.Unlock()
}

func TestACPRegistryPublicationReentryFollowsTheCompleteAdmittedBatch(t *testing.T) {
	b, sink, queue := newRegistryPublicationBase(t)
	sink.hook = func() {
		require.NoError(t, b.ApplyRegistryObservation(&SubagentObservation{RegistryOnly: true, RowKey: "reentrant", Status: bgtask.StatusRunning}))
		b.FlushRegistryObservations()
	}
	require.NoError(t, b.ApplyRegistryObservation(&SubagentObservation{RegistryOnly: true, RowKey: "first", Status: bgtask.StatusSucceeded, CloseRow: true}))
	require.NoError(t, queue.Flush())
	assert.Equal(t, []registryPublicationEvent{
		{operation: "upsert", key: "first", committed: true},
		{operation: "close", key: "first", committed: true},
		{operation: "upsert", key: "reentrant", committed: true},
	}, sink.snapshot(), "reentry must append after every stage of the original batch")
}

func TestACPRegistryPublicationBufferedFailedClearReleasesItsSessionLease(t *testing.T) {
	var owner *testAgent
	var duringDispatch []registryPublicationEvent
	sink := &registryPublicationSink{ControlSink: &agenttest.ControlSink{}}
	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			owner.RouteSessionNotification(SessionNotification{
				ResolveSession: func(json.RawMessage) (string, bool) { return "session-1", true },
				Dispatch: func(string, json.RawMessage) {
					owner.main().applySubagentObservation(&SubagentObservation{RegistryOnly: true, RowKey: "buffered", Status: bgtask.StatusRunning})
					_ = owner.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"buffered-native-row"}`)}, agent.SpanInfo{})
					duringDispatch = sink.snapshot()
				},
			})
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Session creation refused."}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	owner = a
	services := agent.NewProviderServices(sink)
	queue := agent.OutputQueueFor(services)
	a.sink = agent.NewOrderedProviderServices(queue, services)
	observerUnlocked := false
	sink.hook = func() {
		if a.sessionMu.TryLock() {
			observerUnlocked = true
			a.sessionMu.Unlock()
		}
		if !a.sessionUpdates.mu.TryLock() {
			observerUnlocked = false
		} else {
			a.sessionUpdates.mu.Unlock()
		}
	}
	// The replay happens in newSessionLocked's error defer, before ClearContext releases sessionMu.
	// Its queue must publish only after ClearContext releases the complete replacement lease.
	_, err := a.ClearContext()
	require.ErrorContains(t, err, "Session creation refused.")
	assert.Empty(t, duringDispatch, "ordinary replay persistence must not invoke registry observers under source locks")
	require.True(t, observerUnlocked, "buffered replay must not publish an observer through the failed clear's session lease")
	assert.Equal(t, []registryPublicationEvent{{operation: "upsert", key: "buffered", committed: true}}, sink.snapshot())
	assert.False(t, queue.HasPending())
	assert.Equal(t, "session-1", a.CurrentSessionID())
}
