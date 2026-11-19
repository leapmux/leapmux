package kimi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type kimiChildRegistrationTree struct {
	mu         sync.Mutex
	current    map[string]*kimiRegisteredChildServices
	violations []string
}

type kimiRegisteredChildServices struct {
	agent.ProviderServices
	tree     *kimiChildRegistrationTree
	parent   *kimiRegisteredChildServices
	id       string
	physical *byte
}

func (s *kimiRegisteredChildServices) currentLocked() bool {
	if s.parent == nil {
		return true
	}
	current := s.tree.current[s.id]
	return current != nil && current.physical == s.physical && s.parent.currentLocked()
}

func (s *kimiRegisteredChildServices) checkParent(childID string) {
	s.tree.mu.Lock()
	defer s.tree.mu.Unlock()
	if child := s.tree.current[childID]; child != nil && child.parent.physical != s.physical {
		s.tree.violations = append(s.tree.violations, fmt.Sprintf("%s resolved child %s owned by %s", s.id, childID, child.parent.id))
	}
}

func (s *kimiRegisteredChildServices) AcquireChildSink(childID string) agent.ChildSinkLease {
	return s.acquireChild(childID, s.ProviderServices.AcquireChildSink)
}

func (s *kimiRegisteredChildServices) AcquireChildStateSink(childID string) agent.ChildSinkLease {
	return s.acquireChild(childID, s.ProviderServices.AcquireChildStateSink)
}

func (s *kimiRegisteredChildServices) acquireChild(childID string, acquire func(string) agent.ChildSinkLease) agent.ChildSinkLease {
	s.tree.mu.Lock()
	defer s.tree.mu.Unlock()
	if !s.currentLocked() {
		s.tree.violations = append(s.tree.violations, "acquired a child from retired parent "+s.id)
		return agent.ChildSinkLease{}
	}
	lease := acquire(childID)
	direct, valid := agent.DirectTranscript(lease.Sink).(agent.ProviderServices)
	if !valid {
		return agent.ChildSinkLease{}
	}
	child := &kimiRegisteredChildServices{ProviderServices: direct, tree: s.tree, parent: s, id: childID}
	child.physical = new(byte)
	if previous := s.tree.current[childID]; previous != nil && previous.currentLocked() {
		child.physical = previous.physical
	}
	s.tree.current[childID] = child
	lease.Sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(lease.Sink), child)
	cleanup := lease.Cleanup
	lease.Cleanup = func() bool {
		s.tree.mu.Lock()
		defer s.tree.mu.Unlock()
		if !child.currentLocked() || !cleanup() {
			return false
		}
		delete(s.tree.current, childID)
		return true
	}
	return lease
}

func (s *kimiRegisteredChildServices) ChildSink(childID string) agent.ProviderServices {
	s.checkParent(childID)
	return s.ProviderServices.ChildSink(childID)
}

func (s *kimiRegisteredChildServices) PersistChildPrompt(childID, prompt string) error {
	s.checkParent(childID)
	return s.ProviderServices.PersistChildPrompt(childID, prompt)
}

func (s *kimiRegisteredChildServices) PersistChildUserMessage(childID, prompt string) error {
	s.checkParent(childID)
	return s.ProviderServices.PersistChildUserMessage(childID, prompt)
}

func TestKimiNestedChildWritesKeepTheirDirectParent(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	tree := &kimiChildRegistrationTree{current: make(map[string]*kimiRegisteredChildServices)}
	rig.agent.sink = &kimiRegisteredChildServices{ProviderServices: rig.agent.sink, tree: tree, id: "root"}
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-1"}`))
	rig.startPublicTurn(t, "agent-1", "t0", 0)
	rig.publicOpener(t, "agent-1", "t0", `{"kind":"system"}`)
	rig.endPublicTurn(t, "agent-1", "t0", 0, "completed")
	rig.startPublicTurn(t, "agent-1", "t1", 1)
	rig.publicOpener(t, "agent-1", "t1", `{"kind":"system"}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"ended","outcome":"failed","error":"Nested review failed.","detached":false,"child_agent_id":"agent-1"}`))
	tree.mu.Lock()
	assert.Empty(t, tree.violations)
	tree.mu.Unlock()
	parent, _ := rig.sink.BackgroundTask("session_1/agent-0")
	nested, exists := rig.sink.BackgroundTask("session_1/agent-1")
	require.True(t, exists)
	child := rig.sink.Child(parent.ChildAgentID).Child(nested.ChildAgentID)
	var failure bool
	for _, message := range child.Messages() {
		failure = failure || strings.Contains(string(message.Content), "Nested review failed.")
	}
	assert.True(t, failure, "the native failure must reach the actual nested child")
}

func TestKimiNestedChildRenewsAfterItsParentLifetime(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		t.Run(fmt.Sprintf("descendant_already_retired=%t", retired), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			tree := &kimiChildRegistrationTree{current: make(map[string]*kimiRegisteredChildServices)}
			rig.agent.sink = &kimiRegisteredChildServices{ProviderServices: rig.agent.sink, tree: tree, id: "root"}
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-1"}`))
			if retired {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-1"}`))
			}
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			rig.startPublicTurn(t, "agent-0", "t1", 0)
			rig.publicOpener(t, "agent-0", "t1", `{"kind":"user"}`)
			rig.startPublicTurn(t, "agent-1", "t1", 0)
			rig.publicOpener(t, "agent-1", "t1", `{"kind":"user"}`)
			rig.conversationAgentFrame(t, "agent-1", `{"type":"item.ended","turn_id":"t1","item":{"type":"tool_call","item_id":"read","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Read","input":{},"output":"renewed nested output"}}`)
			require.NoError(t, agent.FlushPendingOutput(rig.agent.run("agent-1").childLifetime.writer))
			row, exists := rig.sink.BackgroundTask("session_1/agent-1")
			require.True(t, exists)
			tree.mu.Lock()
			registered := tree.current[row.ChildAgentID]
			current := registered != nil && registered.currentLocked()
			assert.True(t, current, "the renewed parent must own the grandchild's current writer")
			assert.Empty(t, tree.violations)
			tree.mu.Unlock()
			parent, parentFound := rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, parentFound)
			rows := rig.sink.Child(parent.ChildAgentID).Child(row.ChildAgentID).Messages()
			require.Len(t, rows, 2)
			assert.True(t, rows[1].Closing)
			assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
			assert.Contains(t, string(rows[1].Content), "renewed nested output")
		})
	}
}

func TestKimiParentCleanupPreservesDescendantMedia(t *testing.T) {
	t.Parallel()
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent_resumes=%t", resumed), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			tree := &kimiChildRegistrationTree{current: make(map[string]*kimiRegisteredChildServices)}
			rig.agent.sink = &kimiRegisteredChildServices{ProviderServices: rig.agent.sink, tree: tree, id: "root"}
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-1"}`))
			parentRow, _ := rig.sink.BackgroundTask("session_1/agent-0")
			childRow, _ := rig.sink.BackgroundTask("session_1/agent-1")
			child := rig.sink.Child(parentRow.ChildAgentID).Child(childRow.ChildAgentID)
			id, data := kimiMediaTestFile("active grandchild image")
			entered, released := make(chan string, 1), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
				entered <- id
				select {
				case <-released:
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(data)
				case <-request.Context().Done():
				}
			})
			rig.startConversationMediaTurn(t, "agent-1")
			rig.publishConversationMediaResult(t, "agent-1", "image", id)
			awaitKimiMediaRequest(t, entered)
			lifetime := rig.agent.run("agent-1").childLifetime
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.started","turn":{"turn_id":"parent-attempt","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"parent-attempt","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			parentQueue := agent.OutputQueueFor(rig.agent.runSink(rig.agent.run("agent-0")))
			require.True(t, parentQueue.HasPending(), "the parent cleanup waits for its descendant")
			if resumed {
				rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
			}
			tree.mu.Lock()
			registered := tree.current[childRow.ChildAgentID]
			assert.True(t, registered != nil && registered.currentLocked())
			tree.mu.Unlock()
			assert.Same(t, lifetime, rig.agent.run("agent-1").childLifetime, "parent renewal preserves the running descendant's captured scope")
			release()
			waitFor(t, func() bool { return child.MessageCount() == 2 }, "the grandchild's current image result persists")
			message := child.Messages()[1]
			assert.NotEmpty(t, message.SupplementalContent)
			assert.False(t, message.TranscriptOnly)
			rig.conversationAgentFrame(t, "agent-1", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-1"}`))
			if resumed {
				rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
			}
			assert.False(t, parentQueue.HasPending())
			assert.Equal(t, []bool{true, false}, child.TurnActives())
			tree.mu.Lock()
			assert.Empty(t, tree.current)
			assert.Empty(t, tree.violations)
			tree.mu.Unlock()
		})
	}
}

func TestKimiRetiredSessionReleasesDescendantCleanupDependencies(t *testing.T) {
	t.Parallel()
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear_context=%t", clear), func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"agent-0","task_id":"nested","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-1"}`))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			parentRun, childRun := rig.agent.run("agent-0"), rig.agent.run("agent-1")
			waitFor(t, func() bool {
				return parentRun.conversationRecovery.Load() == nil && childRun.conversationRecovery.Load() == nil
			}, "both owned child subscriptions restore their native pages")
			rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-1", `{"type":"turn.started","turn":{"turn_id":"child-attempt","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
			rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.started","turn":{"turn_id":"parent-attempt","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
			rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.ended","turn":{"turn_id":"parent-attempt","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-1").Active)
			rig.agent.dispatchMu.Lock()
			parent := rig.agent.run("agent-0").childLifetime
			graph := parent.graph
			rig.agent.finishDispatch()
			require.True(t, parent.queue.HasPending())
			if clear {
				_, err := rig.agent.ClearContext()
				require.ErrorIs(t, err, agent.ErrContextClearUnsupported)
				assert.False(t, graph.retired.Load(), "requesting Worker replacement must not pretend the old runtime ended")
				assert.True(t, parent.queue.HasPending())
			}
			rig.agent.SetStoppedForTest(true)
			rig.agent.SimulateExitForTest()
			require.NoError(t, rig.agent.Wait())
			assert.True(t, graph.retired.Load())
			assert.False(t, parent.queue.HasPending(), "obsolete cleanup must not block later output")
			graph.mu.Lock()
			assert.Empty(t, graph.nodes)
			graph.mu.Unlock()
			row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "agent-0", "nested"))
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, row.Status, "the Worker still owns process-exit registry finalization")
		})
	}
}

func TestKimiRetiredChildGraphRejectsLateTaskAdoption(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"parent","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	rig.agent.settleAfterExit(agent.MessageCompletionInterrupted)
	assert.NotPanics(t, func() {
		rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"late","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-1"}`))
	})
	_, linked := rig.agent.children.get("agent-1")
	assert.False(t, linked, "late recovery must not install a child in the retired graph")
}

func TestKimiClosedStreamRejectsTaskRecovery(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.closeStream()
	assert.False(t, rig.agent.taskRecoveryWork.Run("late-recovery", func() {}))
	assert.True(t, rig.agent.taskRecoveryWork.Idle())
}

func TestKimiRetiringChildLifetimePreservesNewWork(t *testing.T) {
	t.Parallel()
	for _, userTurn := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent_user_turn=%t", userTurn), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			failure := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
			trace := &kimiChildResetLifecycleSink{ProviderServices: agent.NewModelProgressResetSink(agent.NewProviderServices(failure)), registry: rig.sink}
			rig.agent.sink = trace
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			rig.startPublicTurn(t, "agent-0", "t0", 0)
			failure.closeErr = errors.New("registry storage unavailable")
			rig.endPublicTurn(t, "agent-0", "t0", 0, "completed")
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			require.True(t, rig.agent.run("agent-0").childLifetime.retiring)
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			origin := "system"
			if userTurn {
				origin = "user"
			} else {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"new","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			}
			rig.startPublicTurn(t, "agent-0", "t1", 1)
			rig.publicOpener(t, "agent-0", "t1", fmt.Sprintf(`{"kind":%q}`, origin))
			require.False(t, rig.agent.run("agent-0").childLifetime.retiring)
			failure.closeErr = nil
			require.NoError(t, agent.FlushPendingOutput(rig.agent.sink))
			trace.mu.Lock()
			for _, event := range trace.events {
				assert.NotEqual(t, "cleanup", event.kind, "the old cleanup must refuse the replacement lifetime")
			}
			trace.mu.Unlock()
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"t1","item":{"type":"tool_call","item_id":"read","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Read","input":{"path":"new.go"},"output":"new lifetime output"}}`)
			require.NoError(t, agent.FlushPendingOutput(rig.agent.run("agent-0").childLifetime.writer))
			messages := child.Messages()
			require.NotEmpty(t, messages)
			assert.Contains(t, string(messages[len(messages)-1].Content), "new lifetime output")
			assert.False(t, messages[len(messages)-1].TranscriptOnly)
			rig.endPublicTurn(t, "agent-0", "t1", 1, "completed")
			awaitKimiTestOutput(t, rig.agent.sink)
			if !userTurn {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"new","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			}
			trace.mu.Lock()
			var cleaned int
			for _, event := range trace.events {
				if event.kind == "cleanup" {
					cleaned++
					assert.True(t, event.registryFinal)
				}
			}
			trace.mu.Unlock()
			assert.Equal(t, 1, cleaned)
			assert.Equal(t, []bool{true, false, true, false}, child.TurnActives())
		})
	}
}

func TestKimiChildCleanupWaitsForMediaAndInactivity(t *testing.T) {
	t.Parallel()
	for _, taskFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("task_ends_first=%t", taskFirst), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			trace := &kimiChildResetLifecycleSink{ProviderServices: rig.agent.sink, registry: rig.sink}
			rig.agent.sink = trace
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"reader","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
			rig.startConversationMediaTurn(t, "agent-0")
			id, data := kimiMediaTestFile("retained child image")
			entered, released := make(chan string, 1), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
				entered <- id
				select {
				case <-released:
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(data)
				case <-request.Context().Done():
				}
			})
			rig.publishConversationMediaResult(t, "agent-0", "image", id)
			awaitKimiMediaRequest(t, entered)
			endTask := func() {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"reader","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0"}`))
			}
			if taskFirst {
				endTask()
			}
			rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			if !taskFirst {
				endTask()
			}
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.RoleChildActivity, row.Role)
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
			task, exists := rig.sink.BackgroundTask("session_1/task/main/reader")
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
			assert.Equal(t, bgtask.StatusSucceeded, task.Status)
			assert.Equal(t, []bool{true}, child.TurnActives(), "inactivity waits for the admitted media")
			trace.mu.Lock()
			for _, event := range trace.events {
				assert.NotEqual(t, "cleanup", event.kind)
			}
			trace.mu.Unlock()
			release()
			waitFor(t, func() bool {
				trace.mu.Lock()
				defer trace.mu.Unlock()
				return len(trace.events) > 0 && trace.events[len(trace.events)-1].kind == "cleanup"
			}, "the image, inactivity and captured cleanup drain in order")
			messages := child.Messages()
			require.Len(t, messages, 3)
			assert.Contains(t, string(messages[1].Content), id)
			assert.NotEmpty(t, messages[1].SupplementalContent)
			assert.True(t, messages[2].TurnEnd)
			assert.Equal(t, []bool{true, false}, child.TurnActives())
			trace.mu.Lock()
			events := append([]kimiChildResetLifecycleEvent(nil), trace.events...)
			trace.mu.Unlock()
			require.GreaterOrEqual(t, len(events), 3)
			assert.Equal(t, "state", events[len(events)-2].kind)
			assert.False(t, events[len(events)-2].active)
			assert.True(t, events[len(events)-1].registryFinal)
		})
	}
}
