package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRegistryStorageRefused = errors.New("the registry store refused the write")
var errRegistryPromptRefused = errors.New("the opening prompt store refused the write")

type registryRetryKey struct{ operation, key string }
type registryRetryEvent struct {
	operation, key string
	status         bgtask.Status
	committed      bool
}
type registryRetrySink struct {
	*agenttest.Sink
	mu      sync.Mutex
	blocked map[registryRetryKey]bool
	events  []registryRetryEvent
	hookKey registryRetryKey
	hook    func()
}

func (s *registryRetrySink) block(operation, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked == nil {
		s.blocked = make(map[registryRetryKey]bool)
	}
	s.blocked[registryRetryKey{operation, key}] = true
}
func (s *registryRetrySink) allow(operation, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blocked, registryRetryKey{operation, key})
}
func (s *registryRetrySink) reenterOnce(operation, key string, hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hookKey, s.hook = registryRetryKey{operation, key}, hook
}
func (s *registryRetrySink) apply(operation, key string, status bgtask.Status, run func() error) error {
	s.mu.Lock()
	index := len(s.events)
	s.events = append(s.events, registryRetryEvent{operation: operation, key: key, status: status})
	identity := registryRetryKey{operation, key}
	blocked := s.blocked[identity] || s.blocked[registryRetryKey{operation: operation}]
	var hook func()
	if s.hook != nil && s.hookKey == identity {
		hook, s.hook = s.hook, nil
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if blocked {
		if operation == "prompt" {
			return errRegistryPromptRefused
		}
		return errRegistryStorageRefused
	}
	err := run()
	s.mu.Lock()
	s.events[index].committed = err == nil
	s.mu.Unlock()
	return err
}
func (s *registryRetrySink) snapshot() []registryRetryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}
func (s *registryRetrySink) matching(operation, key string, committed bool) []registryRetryEvent {
	var result []registryRetryEvent
	for _, event := range s.snapshot() {
		if event.operation == operation && event.key == key && (!committed || event.committed) {
			result = append(result, event)
		}
	}
	return result
}
func (s *registryRetrySink) statuses(key string) []bgtask.Status {
	var result []bgtask.Status
	for _, event := range s.matching("upsert", key, true) {
		result = append(result, event.status)
	}
	return result
}
func (s *registryRetrySink) UpsertBackgroundTask(task bgtask.Upsert) error {
	return s.apply("upsert", task.RowKey, task.Status, func() error { return s.Sink.UpsertBackgroundTask(task) })
}
func (s *registryRetrySink) ReviveBackgroundTask(key string) error {
	return s.apply("revive", key, bgtask.StatusRunning, func() error { return s.Sink.ReviveBackgroundTask(key) })
}
func (s *registryRetrySink) CloseBackgroundTask(key string, status bgtask.Status) error {
	return s.apply("close", key, status, func() error { return s.Sink.CloseBackgroundTask(key, status) })
}
func (s *registryRetrySink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return s.apply("message", span.SpanID, 0, func() error { return s.Sink.PersistMessage(source, content, span) })
}
func (s *registryRetrySink) PersistChildPrompt(id, text string) error {
	return s.apply("prompt", id, 0, func() error { return s.Sink.PersistChildPrompt(id, text) })
}

type registryRetryHarness struct {
	agent            *Agent
	base             *agenttest.Sink
	sink             *registryRetrySink
	queue            *agent.OutputQueue
	clock            *quartz.Mock
	created, stopped *quartz.Trap
}

func newRegistryRetryHarness(t *testing.T) *registryRetryHarness {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	a, base := testAgentWithClock(t, clock)
	a.viewWork.CloseAndWait()
	sink := &registryRetrySink{Sink: base}
	services := agent.NewProviderServices(sink)
	queue := agent.OutputQueueFor(services)
	require.NotNil(t, queue)
	created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	queue.ConfigureRetry(t.Context(), clock, nil)
	a.sink = agent.NewOrderedProviderServices(queue, services)
	a.sessions["session"].sink = a.sink
	t.Cleanup(func() { created.Close(); stopped.Close(); queue.StopRetries() })
	return &registryRetryHarness{agent: a, base: base, sink: sink, queue: queue, clock: clock, created: created, stopped: stopped}
}
func (h *registryRetryHarness) recoverOneRetry(t *testing.T) {
	t.Helper()
	require.True(t, h.queue.HasPending(), "the refused operation must remain queued before waiting for its timer")
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, h.created)
	h.created.Close()
	testutil.AdvanceAndAwaitStop(t, ctx, h.clock, delay, h.stopped)
	h.stopped.Close()
	h.queue.StopRetries()
	require.False(t, h.queue.HasPending(), "storage recovery must finish without another native frame")
}
func registryWorkflow(revision, attempt int64, status, outcome string) itemParams {
	children := fmt.Appendf(nil, `[{"childId":"child","attempt":%d,"status":%q`, attempt, status)
	if outcome != "" {
		children = fmt.Appendf(children, `,"terminal":%q`, outcome)
	}
	children = append(children, '}', ']')
	itemStatus := contracts.MuseItemStatusInProgress
	if status == workflowChildFinished {
		itemStatus = contracts.MuseItemStatusCompleted
	}
	return itemParams{SessionID: "session", Item: nativeItem{ID: "workflow", Revision: revision, Kind: contracts.MuseItemKindWorkflow, WorkflowRunID: "run", EntryID: "Registry retry", Status: itemStatus, Children: children}}
}
func feedRegistryWorkflow(t *testing.T, h *registryRetryHarness, item itemParams) []byte {
	t.Helper()
	method := contracts.MuseMethodItemUpdated
	if item.Item.Status != contracts.MuseItemStatusInProgress {
		method = contracts.MuseMethodItemCompleted
	}
	return feed(t, h.agent, method, item)
}
func requireRegistryStatus(t *testing.T, h *registryRetryHarness, key string, want bgtask.Status) bgtask.Item {
	t.Helper()
	row, found := h.base.BackgroundTask(key)
	require.True(t, found, key)
	require.Equal(t, want, row.Status, key)
	return row
}
func registryChildKey() string { return museChildRowKey("session", "child") }
func registryRunKey() string   { return museWorkflowRowKey("session", "run") }

func TestRegistryRevivalRetriesWithoutAnotherNativeFrame(t *testing.T) {
	for _, status := range []string{workflowChildStarted, workflowChildScheduled} {
		t.Run(status, func(t *testing.T) {
			h := newRegistryRetryHarness(t)
			feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildFinished, "failed"))
			h.sink.block("revive", registryChildKey())
			feedRegistryWorkflow(t, h, registryWorkflow(2, 1, status, ""))
			require.True(t, h.queue.HasPending())
			assert.Empty(t, h.base.RevivedTasks())
			h.sink.allow("revive", registryChildKey())
			h.recoverOneRetry(t)
			want := bgtask.StatusRunning
			if status == workflowChildScheduled {
				want = bgtask.StatusPending
			}
			row := requireRegistryStatus(t, h, registryChildKey(), want)
			assert.True(t, row.EndedAt.IsZero())
			assert.Equal(t, []string{registryChildKey()}, h.base.RevivedTasks())
		})
	}
}
func TestRegistryRetryKeepsOlderAndNewerRevisionsInOrder(t *testing.T) {
	h := newRegistryRetryHarness(t)
	h.sink.block("upsert", registryChildKey())
	feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildStarted, ""))
	require.True(t, h.queue.HasPending())
	feedRegistryWorkflow(t, h, registryWorkflow(2, 0, workflowChildFinished, "completed"))
	assert.Empty(t, h.sink.statuses(registryChildKey()))
	h.sink.allow("upsert", registryChildKey())
	h.recoverOneRetry(t)
	requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusSucceeded)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusSucceeded}, h.sink.statuses(registryChildKey()))
}
func TestRegistryRetryDoesNotRepeatASuccessfulRevival(t *testing.T) {
	h := newRegistryRetryHarness(t)
	feedRegistryWorkflow(t, h, registryWorkflow(1, -1, workflowChildFinished, "failed"))
	h.sink.block("upsert", registryChildKey())
	feedRegistryWorkflow(t, h, registryWorkflow(2, 0, workflowChildStarted, ""))
	require.True(t, h.queue.HasPending())
	require.Len(t, h.sink.matching("revive", registryChildKey(), false), 1)
	h.sink.allow("upsert", registryChildKey())
	h.recoverOneRetry(t)
	requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusRunning)
	assert.Len(t, h.sink.matching("revive", registryChildKey(), false), 1)
}
func TestRegistryPendingNativeOutcomeSurvivesRetirement(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			h := newRegistryRetryHarness(t)
			feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildStarted, ""))
			h.sink.block("upsert", registryChildKey())
			feedRegistryWorkflow(t, h, registryWorkflow(2, 0, workflowChildFinished, outcome))
			require.True(t, h.queue.HasPending())
			h.agent.retireHost(agent.MessageCompletionInterrupted)
			h.sink.allow("upsert", registryChildKey())
			h.recoverOneRetry(t)
			want := bgtask.StatusSucceeded
			if outcome == "failed" {
				want = bgtask.StatusFailed
			}
			requireRegistryStatus(t, h, registryChildKey(), want)
			assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, want}, h.sink.statuses(registryChildKey()))
			assert.Len(t, h.sink.matching("close", registryChildKey(), true), 1)
		})
	}
}
func TestRegistryPendingActiveProjectionCannotReopenAfterRetirement(t *testing.T) {
	for _, operation := range []string{"revive", "upsert"} {
		t.Run(operation, func(t *testing.T) {
			h := newRegistryRetryHarness(t)
			feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildFinished, "failed"))
			h.sink.block(operation, registryChildKey())
			feedRegistryWorkflow(t, h, registryWorkflow(2, 1, workflowChildStarted, ""))
			require.True(t, h.queue.HasPending())
			h.agent.retireHost(agent.MessageCompletionInterrupted)
			h.sink.allow(operation, registryChildKey())
			h.recoverOneRetry(t)
			requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusStopped)
			var operations []string
			for _, event := range h.sink.snapshot() {
				if event.key == registryChildKey() && event.committed {
					operations = append(operations, event.operation)
				}
			}
			require.NotEmpty(t, operations)
			assert.Equal(t, "close", operations[len(operations)-1])
		})
	}
}
func TestRegistryRetirementCloseRetriesWithoutAnotherNativeEvent(t *testing.T) {
	h := newRegistryRetryHarness(t)
	item := registryWorkflow(1, 0, workflowChildStarted, "")
	item.Item.Children = json.RawMessage(`[{"childId":"child","attempt":0,"status":"started"},{"childId":"finished","attempt":0,"status":"terminal","terminal":"completed"}]`)
	feedRegistryWorkflow(t, h, item)
	shell := museBackgroundTaskRowKey("session", "shell")
	feed(t, h.agent, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "shell", Revision: 1, Kind: contracts.MuseItemKindToolCall, Tool: "shell", Background: true, Status: "inProgress"}})
	requireRegistryStatus(t, h, shell, bgtask.StatusRunning)
	h.sink.block("close", registryChildKey())
	h.agent.retireHost(agent.MessageCompletionInterrupted)
	require.True(t, h.queue.HasPending())
	h.sink.allow("close", registryChildKey())
	h.recoverOneRetry(t)
	for _, key := range []string{registryRunKey(), registryChildKey(), shell} {
		requireRegistryStatus(t, h, key, bgtask.StatusStopped)
	}
	requireRegistryStatus(t, h, museChildRowKey("session", "finished"), bgtask.StatusSucceeded)
}
func TestRegistryHeadRetryKeepsOnePendingReconciliation(t *testing.T) {
	h := newRegistryRetryHarness(t)
	for _, key := range []string{registryRunKey(), registryChildKey()} {
		require.NoError(t, h.base.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusStopped}))
	}
	history := json.RawMessage(`{"mode":"inline","items":[{"itemId":"workflow","revision":2,"kind":"workflow","workflowRunId":"run","status":"inProgress","children":[{"childId":"child","attempt":0,"status":"started"}]}]}`)
	h.sink.block("revive", registryChildKey())
	require.NoError(t, h.agent.restoreChildRoutes("session", history, "head"))
	require.True(t, h.queue.HasPending())
	require.NoError(t, h.agent.restoreChildRoutes("session", history, "head"))
	h.sink.allow("revive", registryChildKey())
	h.recoverOneRetry(t)
	requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusRunning)
	for _, key := range []string{registryRunKey(), registryChildKey()} {
		assert.Len(t, h.sink.matching("upsert", key, true), 1)
	}
	require.NoError(t, h.agent.restoreChildRoutes("session", history, "head"))
	assert.False(t, h.queue.HasPending())
	for _, key := range []string{registryRunKey(), registryChildKey()} {
		assert.Len(t, h.sink.matching("upsert", key, true), 1)
	}
}
func TestRegistryRetryCallbackCanReenterNativeOutput(t *testing.T) {
	h := newRegistryRetryHarness(t)
	h.sink.block("upsert", registryChildKey())
	feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildStarted, ""))
	require.True(t, h.queue.HasPending())
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.MuseMethodItemCompleted, "params": registryWorkflow(2, 0, workflowChildFinished, "completed")})
	require.NoError(t, err)
	h.sink.reenterOnce("upsert", registryChildKey(), func() { h.agent.HandleOutput(raw) })
	h.sink.allow("upsert", registryChildKey())
	h.recoverOneRetry(t)
	requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusSucceeded)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusSucceeded}, h.sink.statuses(registryChildKey()))
}
func TestRegistryPublicationWaitsForItsNativeTranscriptRow(t *testing.T) {
	h := newRegistryRetryHarness(t)
	h.sink.block("message", "shell")
	key := museBackgroundTaskRowKey("session", "shell")
	feed(t, h.agent, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "shell", Revision: 1, Kind: contracts.MuseItemKindToolCall, Tool: "shell", Background: true, Status: "completed"}})
	require.True(t, h.queue.HasPending())
	assert.Empty(t, h.sink.matching("upsert", key, false))
	h.sink.allow("message", "shell")
	h.recoverOneRetry(t)
	requireRegistryStatus(t, h, key, bgtask.StatusSucceeded)
	messageIndex, registryIndex := -1, -1
	for index, event := range h.sink.snapshot() {
		if !event.committed {
			continue
		}
		if event.operation == "message" && event.key == "shell" {
			messageIndex = index
		}
		if event.operation == "upsert" && event.key == key {
			registryIndex = index
		}
	}
	require.NotEqual(t, -1, messageIndex)
	require.NotEqual(t, -1, registryIndex)
	assert.Less(t, messageIndex, registryIndex)
}
func TestRegistryMetadataRetriesIndependentlyOfAHardOpeningPromptFailure(t *testing.T) {
	h := newRegistryRetryHarness(t)
	h.sink.block("prompt", "")
	key := museChildRowKey("session", "sub")
	h.sink.block("upsert", key)
	var methods []string
	h.agent.SetStdinForTest(&museTestPeer{agent: h.agent, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		methods = append(methods, method)
		require.Equal(t, methodSessionResume, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"head","history":{"mode":"inline","items":[{"itemId":"spawn","kind":"subagent","revision":1,"status":"completed","subagentId":"sub","childSessionId":"native-child","objective":"Original immutable assignment"}]}}`)}
	}})
	err := h.agent.openSession("session", h.agent.APITimeout())
	require.ErrorIs(t, err, errRegistryPromptRefused)
	require.True(t, h.queue.HasPending())
	child := h.agent.sessions["native-child"]
	require.NotNil(t, child)
	opening := child.openingPrompt
	require.NotNil(t, opening)
	assert.Equal(t, "Original immutable assignment", opening.text)
	assert.Equal(t, childViewPrefixFailed, child.childViewPrefix)
	assert.False(t, child.childViewRequested)
	h.sink.allow("upsert", key)
	h.recoverOneRetry(t)
	row := requireRegistryStatus(t, h, key, bgtask.StatusSucceeded)
	assert.Equal(t, child.childID, row.ChildAgentID)
	assert.Len(t, h.sink.matching("prompt", child.childID, false), 1)
	assert.Empty(t, h.base.Child(child.childID).Messages())
	assert.Equal(t, []string{methodSessionResume}, methods)
	assert.Same(t, opening, child.openingPrompt)
}
func TestRegistryRepeatedRefusalKeepsOneAdvancingBatch(t *testing.T) {
	h := newRegistryRetryHarness(t)
	h.sink.block("upsert", registryChildKey())
	feedRegistryWorkflow(t, h, registryWorkflow(1, 0, workflowChildStarted, ""))
	require.True(t, h.queue.HasPending())
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, h.created)
	for range 3 {
		before := len(h.sink.matching("upsert", registryChildKey(), false))
		testutil.AdvanceAndAwaitStop(t, ctx, h.clock, delay, h.stopped)
		delay = testutil.WaitForTimer(t, ctx, h.created)
		assert.Len(t, h.sink.matching("upsert", registryChildKey(), false), before+1)
		assert.Len(t, h.sink.matching("upsert", registryRunKey(), true), 1)
	}
	h.sink.allow("upsert", registryChildKey())
	h.created.Close()
	testutil.AdvanceAndAwaitStop(t, ctx, h.clock, delay, h.stopped)
	h.stopped.Close()
	h.queue.StopRetries()
	assert.False(t, h.queue.HasPending())
	requireRegistryStatus(t, h, registryChildKey(), bgtask.StatusRunning)
	assert.Len(t, h.sink.matching("upsert", registryChildKey(), true), 1)
}
