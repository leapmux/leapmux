package acp

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPOrdinaryChildFinalRegistryWriteRecoversWithoutAnotherNativeFrame(t *testing.T) {
	for _, operation := range []string{"upsert", "close"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			sink.UpdateSessionID("session-1")
			b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
				return &SubagentObservation{
					RowKey: call.ToolCallID, ChildAgentKey: call.ToolCallID,
					ChildAgentSessionID: "session-1", Title: "Original helper", Prompt: "Read the original task.",
					Status: bgtask.StatusRunning, Spawns: true,
				}
			}
			finalStatus := bgtask.StatusSucceeded
			mode := ModeCloseOnly
			if operation == "upsert" {
				finalStatus, mode = bgtask.StatusFailed, ModeUpsert
			}
			var observed *SubagentObservation
			b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
				if !StatusIsFinal(call.Status) {
					return nil
				}
				observed = &SubagentObservation{
					RowKey: call.ToolCallID, Title: "Native final helper", Activity: "Native final activity",
					Status: finalStatus, CloseRow: true, Mode: mode,
				}
				return observed
			}
			b.handleACPSessionUpdate(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"ordinary-call","title":"spawn","status":"pending","kind":"other"}`))
			opened, found := sink.BackgroundTask("ordinary-call")
			require.True(t, found)
			require.NotEmpty(t, opened.ChildAgentID)
			child := sink.Child(opened.ChildAgentID)
			before := child.Messages()
			require.Len(t, before, 1)
			assert.JSONEq(t, `{"content":"Read the original task."}`, string(before[0].Content))
			sink.mu.Lock()
			sink.blocked = operation
			sink.events = nil
			sink.mu.Unlock()

			status := "completed"
			if finalStatus == bgtask.StatusFailed {
				status = "failed"
			}
			final, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "ordinary-call", "status": status,
				"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": "Original native final result."}}},
			})
			require.NoError(t, err)
			b.handleACPSessionUpdate(sessionUpdate(t, "session-1", string(final)))
			require.NotNil(t, observed)
			assert.True(t, queue.HasPending(), "the final registry operation must remain owned after storage refuses it")
			for _, message := range sink.Messages() {
				assert.False(t, message.SpanID == "ordinary-call" && message.Closing, "the parent result waits behind its final registry batch")
			}
			for _, event := range sink.snapshot() {
				assert.NotEqual(t, "cleanup", event.operation, "child cleanup must wait for the accepted final registry stages")
			}

			// A later retry must use the accepted observation and the original
			// delegate, even when the provider's current services have changed.
			observed.Status, observed.Title, observed.Activity = bgtask.StatusStopped, "Mutated helper", "Mutated activity"
			replacement := &agenttest.ControlSink{}
			replacement.UpdateSessionID("session-2")
			replacementServices := agent.NewProviderServices(replacement)
			b.SetSinkForTest(agent.NewOrderedProviderServices(agent.OutputQueueFor(replacementServices), replacementServices))
			sink.allow()
			require.NoError(t, queue.Flush())
			assert.False(t, queue.HasPending())
			finished, found := sink.BackgroundTask("ordinary-call")
			require.True(t, found)
			assert.Equal(t, finalStatus, finished.Status)
			assert.Equal(t, opened.ChildAgentID, finished.ChildAgentID)
			if operation == "upsert" {
				assert.Equal(t, "Native final helper", finished.Title)
				assert.Equal(t, "Native final activity", finished.ActiveForm)
			}
			assert.Empty(t, replacement.BackgroundTasks(), "a retained final operation cannot resolve a replacement delegate")
			assert.Equal(t, before, child.Messages(), "registry recovery must preserve the original child prompt")
			var originalResults []string
			for _, message := range sink.Messages() {
				if message.SpanID == "ordinary-call" && message.Closing {
					originalResults = append(originalResults, string(message.Content))
					assert.Equal(t, "session-1", message.AgentSessionID)
				}
			}
			require.Len(t, originalResults, 1)
			assert.JSONEq(t, string(final), originalResults[0])
			var committed []registryPublicationEvent
			for _, event := range sink.snapshot() {
				if event.committed {
					committed = append(committed, event)
				}
			}
			expected := []registryPublicationEvent{{operation: "close", key: "ordinary-call", committed: true}, {operation: "cleanup", key: opened.ChildAgentID, committed: true}}
			if operation == "upsert" {
				expected = append([]registryPublicationEvent{{operation: "upsert", key: "ordinary-call", committed: true}}, expected...)
			}
			assert.Equal(t, expected, committed, "each native final stage must commit once before child cleanup")
			require.NoError(t, queue.Flush())
			again, found := sink.BackgroundTask("ordinary-call")
			require.True(t, found)
			assert.Equal(t, finished, again)
		})
	}
}

func TestACPOrdinaryOpeningAndRenameRemainSynchronousBeforeRetainedFinalization(t *testing.T) {
	for _, refuseRename := range []bool{false, true} {
		name := "successful rename"
		if refuseRename {
			name = "refused rename"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			var blocked atomic.Bool
			blocked.Store(true)
			queue.Append(agent.OutputWrite{Label: "earlier root output", Run: func() error {
				if blocked.Load() {
					return assert.AnError
				}
				return nil
			}})
			t.Cleanup(func() { blocked.Store(false); sink.allow(); _ = queue.Flush() })
			b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
				return &SubagentObservation{RowKey: call.ToolCallID, ChildSpawnSpanID: call.ToolCallID, Title: "Source helper", Prompt: "Keep the original prompt.", Status: bgtask.StatusRunning, Spawns: true}
			}
			b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
				return &SubagentObservation{RowKey: "native-child", RenameFrom: call.ToolCallID, ChildSpawnSpanID: call.ToolCallID, ChildAgentKey: "native-child", Status: bgtask.StatusSucceeded, CloseRow: true, Mode: ModeCloseOnly}
			}
			b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"span-only-call","title":"spawn","status":"pending"}`))
			source, found := sink.BackgroundTask("span-only-call")
			require.True(t, found, "opening creates the source before native adoption even when root output is pending")
			require.NotEmpty(t, source.ChildAgentID)
			child := sink.Child(source.ChildAgentID)
			before := child.Messages()
			require.Len(t, before, 1)
			if refuseRename {
				sink.mu.Lock()
				sink.blocked = "rename"
				sink.mu.Unlock()
			}
			b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"span-only-call","status":"completed"}`))
			key := "native-child"
			absent := "span-only-call"
			if refuseRename {
				key, absent = absent, key
			}
			pending, found := sink.BackgroundTask(key)
			require.True(t, found)
			assert.Equal(t, source.ChildAgentID, pending.ChildAgentID)
			assert.Equal(t, bgtask.StatusRunning, pending.Status)
			_, found = sink.BackgroundTask(absent)
			assert.False(t, found, "rename fallback cannot manufacture the destination or leave a duplicate source")
			blocked.Store(false)
			sink.allow()
			require.NoError(t, queue.Flush())
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, key, rows[0].RowKey)
			assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
			assert.Equal(t, source.ChildAgentID, rows[0].ChildAgentID)
			assert.Equal(t, before, child.Messages())
			assert.Equal(t, []string{source.ChildAgentID}, sink.ChildAgentIDs())
		})
	}
}

type ordinaryEnsureRefusalServices struct {
	agent.ProviderServices
	refused atomic.Bool
	calls   atomic.Int64
}

func (services *ordinaryEnsureRefusalServices) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	services.calls.Add(1)
	if services.refused.Load() {
		return "", assert.AnError
	}
	return services.ProviderServices.EnsureChildAgent(spec)
}

func TestACPOrdinaryFinalRegistryRecoveryDoesNotRetryFailedChildCreation(t *testing.T) {
	t.Parallel()
	b, sink, queue := newRegistryPublicationBase(t)
	services := &ordinaryEnsureRefusalServices{ProviderServices: b.sink}
	services.refused.Store(true)
	b.SetSinkForTest(services)
	b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
		return &SubagentObservation{RowKey: call.ToolCallID, ChildAgentKey: call.ToolCallID, Status: bgtask.StatusRunning, Spawns: true}
	}
	b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
		return &SubagentObservation{RowKey: call.ToolCallID, Status: bgtask.StatusFailed, CloseRow: true}
	}
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"unresolved-call","title":"spawn","status":"pending"}`))
	assert.Equal(t, int64(1), services.calls.Load())
	assert.False(t, b.childIdentityRefused("unresolved-call"), "a storage refusal is not an identity refusal")
	sink.mu.Lock()
	sink.blocked = "upsert"
	sink.mu.Unlock()
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"unresolved-call","status":"failed"}`))
	require.True(t, queue.HasPending())
	services.refused.Store(false)
	sink.allow()
	require.NoError(t, queue.Flush())
	assert.Equal(t, int64(1), services.calls.Load(), "registry retry must not repeat child resolution")
	assert.Empty(t, sink.ChildAgentIDs())
	row, found := sink.BackgroundTask("unresolved-call")
	require.True(t, found)
	assert.Empty(t, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.False(t, b.FeedChildUpdate("unresolved-call", json.RawMessage(`{"sessionUpdate":"plan","entries":[]}`)))
}

func TestACPOrdinaryInvalidClosePreservesItsChildAndCannotBlockRootOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status bgtask.Status
	}{{"unspecified", bgtask.StatusUnspecified}, {"pending", bgtask.StatusPending}, {"running", bgtask.StatusRunning}, {"paused", bgtask.StatusPaused}, {"unknown", bgtask.Status(255)}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
				return &SubagentObservation{RowKey: call.ToolCallID, ChildAgentKey: call.ToolCallID, Prompt: "Preserve this child.", Status: bgtask.StatusRunning, Spawns: true}
			}
			b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
				return &SubagentObservation{RowKey: call.ToolCallID, Status: tc.status, CloseRow: true, Mode: ModeCloseOnly, Report: agent.SubagentReport{Text: "Refuse this invalid report."}}
			}
			b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"invalid-close","title":"spawn","status":"pending"}`))
			before, found := sink.BackgroundTask("invalid-close")
			require.True(t, found)
			child := sink.Child(before.ChildAgentID)
			prompt := child.Messages()
			sink.mu.Lock()
			sink.events = nil
			sink.mu.Unlock()
			b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"invalid-close","status":"completed"}`))
			after, found := sink.BackgroundTask("invalid-close")
			require.True(t, found)
			assert.Equal(t, before, after)
			assert.Equal(t, prompt, child.Messages())
			assert.Empty(t, child.LeapMuxNotifications())
			assert.Empty(t, sink.snapshot(), "invalid closure admits neither registry writes nor cleanup")
			assert.False(t, queue.HasPending())
			assert.True(t, b.FeedChildUpdate("invalid-close", json.RawMessage(`{"sessionUpdate":"plan","entries":[]}`)), "invalid closure must not forget the accepted route")
			require.NoError(t, b.Sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"healthy":"later output"}`)}, agent.SpanInfo{}))
			assert.False(t, queue.HasPending())
		})
	}
}

type ordinaryNestedRegistryServices struct {
	agent.ProviderServices
	probe *registryPublicationSink
	child bool
}

func (services *ordinaryNestedRegistryServices) ChildSink(childID string) agent.ProviderServices {
	return &ordinaryNestedRegistryServices{ProviderServices: services.ProviderServices.ChildSink(childID), probe: services.probe, child: true}
}

func (services *ordinaryNestedRegistryServices) CloseBackgroundTask(key string, status bgtask.Status) error {
	if !services.child {
		return services.ProviderServices.CloseBackgroundTask(key, status)
	}
	return services.probe.apply("nested-close", key, func() error {
		return services.ProviderServices.CloseBackgroundTask(key, status)
	})
}

func (services *ordinaryNestedRegistryServices) CleanupChildAgent(childID string) {
	if !services.child {
		services.ProviderServices.CleanupChildAgent(childID)
		return
	}
	_ = services.probe.apply("nested-cleanup", childID, func() error {
		services.ProviderServices.CleanupChildAgent(childID)
		return nil
	})
}

func TestACPServedChildIncompleteRegistryFinalizationUsesTheRootQueue(t *testing.T) {
	t.Parallel()
	b, sink, queue := newRegistryPublicationBase(t)
	b.SetSinkForTest(&ordinaryNestedRegistryServices{ProviderServices: b.sink, probe: sink})
	b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
		return &SubagentObservation{RowKey: call.ToolCallID, ChildAgentKey: call.ToolCallID, Title: call.Title, Prompt: "Keep the native child prompt.", Status: bgtask.StatusRunning, Spawns: true}
	}
	b.hooks.SubagentFromToolCallUpdate = func(call ToolCallUpdateEnvelope) *SubagentObservation {
		return &SubagentObservation{RowKey: call.ToolCallID, Status: FinalStatus(call.Status), CloseRow: true, Mode: ModeCloseOnly}
	}
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"parent-call","title":"Parent helper","status":"pending"}`))
	parent, found := sink.BackgroundTask("parent-call")
	require.True(t, found)
	require.True(t, b.AttachChildSession(ChildSessionTarget{RootSessionID: "session-1", SessionID: "served-child-session", RowKey: "parent-call"}))
	const opening = `{"sessionUpdate":"tool_call","toolCallId":"nested-call","title":"Nested helper","status":"pending"}`
	b.HandleSessionUpdateForTest(sessionUpdate(t, "served-child-session", opening))
	nested, found := sink.BackgroundTask("nested-call")
	require.True(t, found)
	assert.Equal(t, parent.ChildAgentID, nested.ParentAgentID)
	childServices, served := b.SessionOwner("served-child-session")
	require.True(t, served)
	childQueue := agent.OutputQueueFor(childServices)
	require.NotNil(t, childQueue)
	require.NotSame(t, queue, childQueue, "the native child route owns its own transcript queue")
	sink.mu.Lock()
	sink.blocked = "nested-close"
	sink.events = nil
	sink.mu.Unlock()
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"parent-call","status":"completed"}`))
	require.True(t, queue.HasPending(), "nested final registry writes must precede the physical root's exit sweep")
	assert.False(t, childQueue.HasPending(), "registry finalization must not wait in the child transcript queue")
	beforeRecovery, found := sink.BackgroundTask("nested-call")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, beforeRecovery.Status)
	for _, event := range sink.snapshot() {
		assert.False(t, event.committed && (event.operation == "cleanup" || event.operation == "nested-cleanup"))
	}
	// Session retirement must not replace the already admitted native outcome.
	// Its actual Base path also removes the routes before storage recovers.
	b.retireSession("session-1")
	sink.allow()
	require.NoError(t, queue.Flush())
	finished, found := sink.BackgroundTask("nested-call")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, finished.Status, "a successful parent leaves its incomplete nested tool failed")
	assert.Equal(t, nested.ChildAgentID, finished.ChildAgentID)
	child := sink.Child(parent.ChildAgentID)
	var nativeClosings []agenttest.Message
	for _, message := range child.Messages() {
		if message.SpanID == "nested-call" && message.Closing {
			nativeClosings = append(nativeClosings, message)
		}
	}
	require.Len(t, nativeClosings, 1)
	assert.JSONEq(t, opening, string(nativeClosings[0].Content))
	assert.Equal(t, agent.MessageCompletionError, nativeClosings[0].Completion)
	var committed []registryPublicationEvent
	for _, event := range sink.snapshot() {
		if event.committed {
			committed = append(committed, event)
		}
	}
	assert.Equal(t, []registryPublicationEvent{
		{operation: "nested-close", key: "nested-call", committed: true},
		{operation: "nested-cleanup", key: nested.ChildAgentID, committed: true},
		{operation: "close", key: "parent-call", committed: true},
		{operation: "cleanup", key: parent.ChildAgentID, committed: true},
	}, committed)
}

func TestACPOrdinaryFinishedUpsertWithoutCloseRetainsOnlyItsMetadata(t *testing.T) {
	t.Parallel()
	b, sink, queue := newRegistryPublicationBase(t)
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "finished-upsert", Title: "Opening title", Status: bgtask.StatusRunning})
	sink.mu.Lock()
	sink.blocked = "upsert"
	sink.events = nil
	sink.mu.Unlock()
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "finished-upsert", Title: "Native final title", Status: bgtask.StatusFailed})
	require.True(t, queue.HasPending())
	sink.allow()
	require.NoError(t, queue.Flush())
	row, found := sink.BackgroundTask("finished-upsert")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Equal(t, "Native final title", row.Title)
	var committed []registryPublicationEvent
	for _, event := range sink.snapshot() {
		if event.committed {
			committed = append(committed, event)
		}
	}
	assert.Equal(t, []registryPublicationEvent{{operation: "upsert", key: "finished-upsert", committed: true}}, committed)
}

func TestACPOrdinaryReportOnlyFinalStatusDoesNotCloseItsRegistryRow(t *testing.T) {
	t.Parallel()
	b, sink, queue := newRegistryPublicationBase(t)
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "report-only", ChildAgentKey: "report-only", Status: bgtask.StatusRunning})
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "report-only", Status: bgtask.StatusSucceeded, Mode: ModeCloseOnly})
	assert.False(t, queue.HasPending())
	row, found := sink.BackgroundTask("report-only")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []string{"report-only"}, b.openRows.takeAll(), "a final status with no registry primitive cannot claim a close")
}

func TestACPOrdinaryFirstFinalSpanOnlyChildKeepsItsPromptAndNativeReport(t *testing.T) {
	t.Parallel()
	b, sink, _ := newRegistryPublicationBase(t)
	var observation *SubagentObservation
	b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
		observation = &SubagentObservation{
			RowKey: call.ToolCallID, ChildSpawnSpanID: call.ToolCallID,
			Title: "Already finished helper", Prompt: "Read the original task.",
			Status: bgtask.StatusSucceeded, CloseRow: true,
			ReportID: call.ToolCallID, Report: agent.SubagentReport{Text: "Keep the native final report."},
		}
		return observation
	}
	const native = `{"sessionUpdate":"tool_call","toolCallId":"first-final-call","title":"spawn","status":"completed"}`
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", native))
	row, found := sink.BackgroundTask("first-final-call")
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	child := sink.Child(row.ChildAgentID)
	messages := child.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, `{"content":"Read the original task."}`, string(messages[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Keep the native final report.", reports[0]["text"])
	b.ApplySubagentObservation(observation)
	again, found := sink.BackgroundTask("first-final-call")
	require.True(t, found)
	assert.Equal(t, row.ChildAgentID, again.ChildAgentID)
	assert.Len(t, child.LeapMuxNotifications(), 1, "the original ReportID deduplicates a repeated observation")
	var nativeResults []string
	for _, message := range sink.Messages() {
		if message.SpanID == "first-final-call" && message.Closing {
			nativeResults = append(nativeResults, string(message.Content))
		}
	}
	require.Len(t, nativeResults, 1)
	assert.JSONEq(t, native, nativeResults[0])
}

func TestACPFinalRegistryAdmissionRejectsInvalidIdentityOrStatus(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		status    bgtask.Status
	}{{"empty key", "", bgtask.StatusSucceeded}, {"nonfinal status", "row", bgtask.StatusRunning}, {"unknown status", "row", bgtask.Status(255)}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			err := b.main().appendFinalRegistryObservation(tc.key, SubagentObservation{Status: tc.status, CloseRow: true}, "", childAgentRef{})
			require.Error(t, err)
			assert.False(t, queue.HasPending())
			assert.Empty(t, sink.snapshot())
			assert.Empty(t, b.openRows.takeAll())
		})
	}
}

func TestACPValidatedChildReportFallbackDoesNotBypassARegistryReadFailure(t *testing.T) {
	t.Parallel()
	b, sink, _ := newRegistryPublicationBase(t)
	observation := &SubagentObservation{
		RowKey: "unreadable-final", ChildSpawnSpanID: "unreadable-final",
		Status: bgtask.StatusSucceeded, CloseRow: true,
		ReportID: "unreadable-report", Report: agent.SubagentReport{Text: "Keep the validated report identity."},
	}
	b.hooks.SubagentFromToolCall = func(ToolCallEnvelope) *SubagentObservation { return observation }
	sink.LookupErr = assert.AnError
	b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", `{"sessionUpdate":"tool_call","toolCallId":"unreadable-final","status":"completed"}`))
	row, found := sink.BackgroundTask("unreadable-final")
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	child := sink.Child(row.ChildAgentID)
	assert.Empty(t, child.LeapMuxNotifications(), "a failed read cannot authorize the missing-link fallback")
	sink.LookupErr = nil
	b.ApplySubagentObservation(observation)
	again, found := sink.BackgroundTask("unreadable-final")
	require.True(t, found)
	assert.Equal(t, row.ChildAgentID, again.ChildAgentID)
	require.Len(t, child.LeapMuxNotifications(), 1)
	assert.Equal(t, "Keep the validated report identity.", child.LeapMuxNotifications()[0]["text"])
}

func TestACPOrdinaryFirstFinalResultWaitsForItsRegistryBatch(t *testing.T) {
	for _, operation := range []string{"upsert", "close"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			b, sink, queue := newRegistryPublicationBase(t)
			sink.UpdateSessionID("session-1")
			t.Cleanup(func() { sink.allow(); _ = queue.Flush() })
			b.hooks.SubagentFromToolCall = func(call ToolCallEnvelope) *SubagentObservation {
				return &SubagentObservation{
					RowKey: call.ToolCallID, ChildSpawnSpanID: call.ToolCallID,
					Title: "Already finished helper", Prompt: "Read the original task.",
					Status: bgtask.StatusSucceeded, CloseRow: true,
					ReportID: call.ToolCallID, Report: agent.SubagentReport{Text: "Keep the native final report."},
				}
			}
			sink.mu.Lock()
			sink.blocked = operation
			sink.mu.Unlock()
			const native = `{"sessionUpdate":"tool_call","toolCallId":"first-final-call","title":"spawn","status":"completed"}`
			b.HandleSessionUpdateForTest(sessionUpdate(t, "session-1", native))
			require.True(t, queue.HasPending(), "the first final observation must retain its refused registry stage")
			for _, message := range sink.Messages() {
				assert.False(t, message.SpanID == "first-final-call" && message.Closing,
					"the first final parent result must wait for its registry batch")
			}
			childIDs := sink.ChildAgentIDs()
			require.Len(t, childIDs, 1)
			childID := childIDs[0]
			child := sink.Child(childID)
			prompt := child.Messages()
			require.Len(t, prompt, 1)
			assert.JSONEq(t, `{"content":"Read the original task."}`, string(prompt[0].Content))
			reports := child.LeapMuxNotifications()
			require.Len(t, reports, 1)
			assert.Equal(t, "Keep the native final report.", reports[0]["text"])
			for _, event := range sink.snapshot() {
				assert.False(t, event.operation == "cleanup" && event.committed,
					"cleanup must wait for the first final registry outcome")
			}

			sink.allow()
			require.NoError(t, queue.Flush())
			assert.False(t, queue.HasPending())
			row, found := sink.BackgroundTask("first-final-call")
			require.True(t, found)
			assert.Equal(t, childID, row.ChildAgentID)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.Equal(t, "Already finished helper", row.Title)
			assert.Equal(t, prompt, child.Messages(), "registry recovery must preserve the original opening prompt")
			assert.Equal(t, reports, child.LeapMuxNotifications(), "registry recovery must not repeat the native report")
			var results []agenttest.Message
			for _, message := range sink.Messages() {
				if message.SpanID == "first-final-call" && message.Closing {
					results = append(results, message)
				}
			}
			require.Len(t, results, 1)
			assert.JSONEq(t, native, string(results[0].Content))
			assert.Equal(t, "session-1", results[0].AgentSessionID)
			var committed []registryPublicationEvent
			for _, event := range sink.snapshot() {
				if event.committed {
					committed = append(committed, event)
				}
			}
			assert.Equal(t, []registryPublicationEvent{
				{operation: "upsert", key: "first-final-call", committed: true},
				{operation: "close", key: "first-final-call", committed: true},
				{operation: "cleanup", key: childID, committed: true},
			}, committed)
		})
	}
}
