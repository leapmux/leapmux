package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// taskPartPage is one page of stored messages that the fake daemon serves.
type taskPartPage struct {
	messages []string
	// next is the cursor of the next older page, or "" for the oldest page.
	next string
}

// taskPartDaemon serves the stored messages of the parent session and the
// session abort, and records what LeapMux asked for.
type taskPartDaemon struct {
	mu          sync.Mutex
	pages       map[string]taskPartPage // keyed by the `before` cursor; "" is the newest page
	reads       []string                // the query of each message read
	aborts      []string                // the session of each abort
	readStatus  int
	abortStatus int
}

func newTaskPartAgent(t *testing.T, pages map[string]taskPartPage) (*Agent, *agenttest.Sink, *taskPartDaemon) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent", ProviderName: "opencode", Ctx: t.Context(), Clock: testutil.NewQuartzMock(t),
	})
	*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
	a.configureQuestions(a.Sink())
	daemon := &taskPartDaemon{pages: pages, readStatus: http.StatusOK, abortStatus: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(daemon.serve(t)))
	t.Cleanup(server.Close)
	a.Questions.baseURL = server.URL
	return a, sink, daemon
}

func (d *taskPartDaemon) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/test-session/message":
			d.mu.Lock()
			d.reads = append(d.reads, r.URL.RawQuery)
			page, known := d.pages[r.URL.Query().Get("before")]
			status := d.readStatus
			d.mu.Unlock()
			assert.Equal(t, "10", r.URL.Query().Get("limit"), "each read takes one page")
			if status != http.StatusOK {
				http.Error(w, "the session is gone", status)
				return
			}
			require.True(t, known, "the reader followed a cursor the daemon never stated")
			if page.next != "" {
				w.Header().Set(openCodeNextCursorHeader, page.next)
			}
			_, _ = fmt.Fprint(w, "["+strings.Join(page.messages, ",")+"]")
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/abort"):
			d.mu.Lock()
			d.aborts = append(d.aborts, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/abort"))
			status := d.abortStatus
			d.mu.Unlock()
			if status != http.StatusOK {
				http.Error(w, "the abort failed", status)
				return
			}
			_, _ = fmt.Fprint(w, "true")
		default:
			t.Errorf("unexpected daemon request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}
}

func (d *taskPartDaemon) recorded() (reads, aborts []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reads...), append([]string(nil), d.aborts...)
}

// storedTaskMessage is a stored assistant message whose task call callID is in
// status, with metadata as its state's metadata ("" for none).
func storedTaskMessage(t *testing.T, callID, status, metadata string) string {
	t.Helper()
	state := map[string]any{"status": status, "input": map[string]any{"prompt": "Count."}}
	if metadata != "" {
		state["metadata"] = json.RawMessage(metadata)
	}
	message, err := json.Marshal(map[string]any{
		"info": map[string]any{"id": "msg-" + callID, "sessionID": "test-session", "role": "assistant"},
		"parts": []any{
			map[string]any{"id": "prt-text-" + callID, "type": "text", "text": "Delegating."},
			map[string]any{"id": "prt-" + callID, "type": "tool", "callID": callID, "tool": "task", "state": state},
		},
	})
	require.NoError(t, err)
	return string(message)
}

func runningTask(t *testing.T, callID, childSession string) string {
	t.Helper()
	return storedTaskMessage(t, callID, "running", fmt.Sprintf(`{"parentSessionId":"test-session","sessionId":%q}`, childSession))
}

const storedUserMessage = `{"info":{"id":"msg-user","sessionID":"test-session","role":"user"},"parts":[{"id":"prt-user","type":"text","text":"Steer the count."}]}`

func TestOpenCodeInterruptChildAbortsTheChildSession(t *testing.T) {
	t.Parallel()
	a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{
		"": {messages: []string{runningTask(t, "call-other", "ses-other"), runningTask(t, "call-1", "ses-child"), storedUserMessage}},
	})
	require.NoError(t, a.InterruptChild("call-1", agent.StopContext{}))
	reads, aborts := daemon.recorded()
	assert.Equal(t, []string{"limit=10"}, reads)
	assert.Equal(t, []string{"ses-child"}, aborts, "only the child session of the stopped call is cancelled")
	assert.True(t, a.takeChildStop("call-1"), "the stop waits for the close of the child's row")
	assert.False(t, a.takeChildStop("call-other"))
}

func TestOpenCodeInterruptChildReadsOlderPages(t *testing.T) {
	t.Parallel()
	a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{
		// A part of an unknown shape and a tool of another name do not end the read.
		"":         {messages: []string{storedUserMessage, `{"info":{"id":"msg-odd"},"parts":[{"type":"tool","callID":7},{"type":"tool","callID":"call-1","tool":"bash","state":{"status":"running"}}]}`}, next: "cursor-1"},
		"cursor-1": {messages: []string{runningTask(t, "call-1", "ses-child")}, next: "cursor-2"},
	})
	require.NoError(t, a.InterruptChild("call-1", agent.StopContext{}))
	reads, aborts := daemon.recorded()
	assert.Equal(t, []string{"limit=10", "before=cursor-1&limit=10"}, reads, "the read stops at the page that holds the call")
	assert.Equal(t, []string{"ses-child"}, aborts)
}

func TestOpenCodeInterruptChildWaitsForTheChildSession(t *testing.T) {
	t.Parallel()
	for name, message := range map[string]func(*testing.T) string{
		"a pending call": func(t *testing.T) string { return storedTaskMessage(t, "call-1", "pending", "") },
		"a running call without metadata": func(t *testing.T) string {
			return storedTaskMessage(t, "call-1", "running", "")
		},
		"a running call without a child session": func(t *testing.T) string {
			return storedTaskMessage(t, "call-1", "running", `{"parentSessionId":"test-session"}`)
		},
		"a running call with an empty child session": func(t *testing.T) string {
			return storedTaskMessage(t, "call-1", "running", `{"sessionId":""}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{"": {messages: []string{message(t)}}})
			assert.ErrorIs(t, a.InterruptChild("call-1", agent.StopContext{}), agent.ErrChildRouteNotReady)
			_, aborts := daemon.recorded()
			assert.Empty(t, aborts)
			assert.False(t, a.takeChildStop("call-1"))
		})
	}
}

func TestOpenCodeInterruptChildOfAnEndedCallStopsNothing(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "error"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{
				"": {messages: []string{storedTaskMessage(t, "call-1", status, `{"sessionId":"ses-child"}`)}},
			})
			require.NoError(t, a.InterruptChild("call-1", agent.StopContext{}))
			_, aborts := daemon.recorded()
			assert.Empty(t, aborts)
			assert.False(t, a.takeChildStop("call-1"))
		})
	}
}

func TestOpenCodeInterruptChildRefusesAnUnknownCall(t *testing.T) {
	t.Parallel()
	t.Run("the oldest page ends the read", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{
			"":         {messages: []string{runningTask(t, "call-other", "ses-other")}, next: "cursor-1"},
			"cursor-1": {messages: []string{storedUserMessage}},
		})
		err := a.InterruptChild("call-1", agent.StopContext{})
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrChildRouteNotReady)
		assert.Contains(t, err.Error(), "call-1")
		reads, aborts := daemon.recorded()
		assert.Len(t, reads, 2)
		assert.Empty(t, aborts)
	})
	t.Run("the page limit ends the read", func(t *testing.T) {
		t.Parallel()
		pages := map[string]taskPartPage{}
		before := ""
		for page := range openCodeTaskPartMaxPages + 2 {
			next := fmt.Sprintf("cursor-%d", page+1)
			pages[before] = taskPartPage{messages: []string{storedUserMessage}, next: next}
			before = next
		}
		a, _, daemon := newTaskPartAgent(t, pages)
		require.Error(t, a.InterruptChild("call-1", agent.StopContext{}))
		reads, aborts := daemon.recorded()
		assert.Len(t, reads, openCodeTaskPartMaxPages)
		assert.Empty(t, aborts)
	})
	t.Run("an empty session", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{"": {}})
		require.Error(t, a.InterruptChild("call-1", agent.StopContext{}))
		_, aborts := daemon.recorded()
		assert.Empty(t, aborts)
	})
}

func TestOpenCodeInterruptChildFailures(t *testing.T) {
	t.Parallel()
	t.Run("a refused abort forgets the stop", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{"": {messages: []string{runningTask(t, "call-1", "ses-child")}}})
		daemon.abortStatus = http.StatusInternalServerError
		err := a.InterruptChild("call-1", agent.StopContext{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
		assert.False(t, a.takeChildStop("call-1"), "a cancel that failed stopped nothing")
	})
	t.Run("a refused message read", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, nil)
		daemon.readStatus = http.StatusNotFound
		err := a.InterruptChild("call-1", agent.StopContext{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
		_, aborts := daemon.recorded()
		assert.Empty(t, aborts)
	})
	t.Run("an unreadable page", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{"": {messages: []string{`{"parts":"none"}`}}})
		require.Error(t, a.InterruptChild("call-1", agent.StopContext{}))
		_, aborts := daemon.recorded()
		assert.Empty(t, aborts)
	})
	t.Run("no daemon address", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, nil)
		a.Questions.baseURL = ""
		err := a.InterruptChild("call-1", agent.StopContext{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "daemon address")
		reads, _ := daemon.recorded()
		assert.Empty(t, reads)
	})
	t.Run("no current session", func(t *testing.T) {
		t.Parallel()
		a, _, daemon := newTaskPartAgent(t, nil)
		a.SetSessionIDForTest("")
		assert.ErrorIs(t, a.InterruptChild("call-1", agent.StopContext{}), agent.ErrChildRouteNotReady)
		reads, _ := daemon.recorded()
		assert.Empty(t, reads)
	})
}

// Two children stop at once. Each stop reaches its own session and waits for
// its own row.
func TestOpenCodeInterruptChildrenConcurrently(t *testing.T) {
	t.Parallel()
	a, _, daemon := newTaskPartAgent(t, map[string]taskPartPage{
		"": {messages: []string{runningTask(t, "call-1", "ses-child-1"), runningTask(t, "call-2", "ses-child-2")}},
	})
	var wg sync.WaitGroup
	for _, call := range []string{"call-1", "call-2"} {
		wg.Go(func() {
			assert.NoError(t, a.InterruptChild(call, agent.StopContext{}))
		})
	}
	wg.Wait()
	_, aborts := daemon.recorded()
	assert.ElementsMatch(t, []string{"ses-child-1", "ses-child-2"}, aborts)
	assert.True(t, a.takeChildStop("call-1"))
	assert.True(t, a.takeChildStop("call-2"))
}

// openCodeTaskFailure is the final ACP update of a task whose child failed.
func openCodeTaskFailure(t *testing.T, callID, childSession string) json.RawMessage {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "failed", "title": "task",
		"rawOutput": map[string]any{"error": "Subagent failed: The operation was aborted.", "metadata": map[string]any{"sessionId": childSession}},
		"content":   []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "Subagent failed: The operation was aborted."}}},
	})
	require.NoError(t, err)
	return frame
}

func TestOpenCodeStoppedChildClosesAsInterrupted(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name    string
		stop    bool
		final   func(*testing.T) json.RawMessage
		closing bgtask.Status
	}{
		{
			name: "a stopped child that the daemon fails", stop: true,
			final:   func(t *testing.T) json.RawMessage { return openCodeTaskFailure(t, "call-1", "ses-child") },
			closing: bgtask.StatusInterrupted,
		},
		{
			name: "a stopped child that completed first", stop: true,
			final:   func(t *testing.T) json.RawMessage { return openCodeTaskResult(t, "call-1", "ses-child", "Counted.") },
			closing: bgtask.StatusSucceeded,
		},
		{
			name:    "a child that failed on its own",
			final:   func(t *testing.T) json.RawMessage { return openCodeTaskFailure(t, "call-1", "ses-child") },
			closing: bgtask.StatusFailed,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newTaskPartAgent(t, map[string]taskPartPage{"": {messages: []string{runningTask(t, "call-1", "ses-child")}}})
			a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count slowly to one hundred."))
			running, found := sink.BackgroundTask("call-1")
			require.True(t, found)
			require.Equal(t, bgtask.StatusRunning, running.Status)
			if scenario.stop {
				require.NoError(t, a.InterruptChild("call-1", agent.StopContext{}))
			}
			a.HandleToolCallUpdateForTest(scenario.final(t))
			closed, found := sink.BackgroundTask("ses-child")
			require.True(t, found, "the final update re-keys the row to the child session")
			assert.Equal(t, scenario.closing, closed.Status)
			assert.False(t, a.takeChildStop("call-1"), "the close consumes the stop")
		})
	}
}

// A stop applies to the close of its own call alone. A tool that is not a
// task, and a later call, keep their native verdicts.
func TestOpenCodeChildStopAppliesToItsOwnCall(t *testing.T) {
	t.Parallel()
	a, _, _ := newTaskPartAgent(t, nil)
	a.recordChildStop("call-1", true)
	other := a.subagentFromToolCallUpdate(failedToolUpdate("call-2"))
	require.NotNil(t, other)
	assert.Equal(t, bgtask.StatusFailed, other.Status)
	stopped := a.subagentFromToolCallUpdate(failedToolUpdate("call-1"))
	require.NotNil(t, stopped)
	assert.Equal(t, bgtask.StatusInterrupted, stopped.Status)
	replayed := a.subagentFromToolCallUpdate(failedToolUpdate("call-1"))
	require.NotNil(t, replayed)
	assert.Equal(t, bgtask.StatusFailed, replayed.Status, "a replay of the close after the stop was read keeps the native verdict")
	assert.Nil(t, a.subagentFromToolCallUpdate(runningShellUpdate("call-3")), "a non-final update of a non-task call is no observation")
}

// failedToolUpdate is the final ACP update of a call that failed.
func failedToolUpdate(callID string) acp.ToolCallUpdateEnvelope {
	return acp.ToolCallUpdateEnvelope{ToolCallID: callID, Status: "failed"}
}

// runningShellUpdate is a non-final ACP update of a shell call.
func runningShellUpdate(callID string) acp.ToolCallUpdateEnvelope {
	return acp.ToolCallUpdateEnvelope{ToolCallID: callID, Status: "in_progress", Title: "bash", RawInput: json.RawMessage(`{"command":"ls"}`)}
}
