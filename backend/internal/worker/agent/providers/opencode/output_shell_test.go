package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeShellOutputRejectsAnotherNativeSession(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "/api/shell/sh_1", r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"sh_1","metadata":{"sessionID":"foreign"}}}`))
	})
	a, sink := assemblyAgent(t, rpc)
	beginNativeShellTestTool(t, a)
	waitNativeShellReaders(t, a)
	for _, update := range sink.ProgressUpdates() {
		assert.NotEqual(t, agent.ProgressOutputTail, update.Operation)
	}
}

func TestNativeShellOutputKeepsItsLocationAfterReadFailure(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	poll := clock.Trap().NewTimer(openCodeShellPollTimerTag)
	defer poll.Close()
	var reads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/project", r.URL.Query().Get("location[directory]"))
		if r.URL.Path == "/api/shell/sh_1" {
			_, _ = w.Write([]byte(`{"data":{"id":"sh_1","metadata":{"sessionID":"ses_1"}}}`))
			return
		}
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"output":"after retry","cursor":11,"size":11,"truncated":false}}`))
	})
	a, _ := assemblyAgent(t, rpc)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: a.Context(), Cancel: a.nativeCancel, Clock: clock})
	updates := beginNativeShellTestTool(t, a)
	ctx := testutil.DeadlineContext(t)
	require.Equal(t, openCodeShellPollInterval, testutil.WaitForTimer(t, ctx, poll))
	a.root.mu.Lock()
	a.root.location.Directory = "/moved"
	a.root.mu.Unlock()
	clock.Advance(openCodeShellPollInterval).MustWait(ctx)
	for {
		select {
		case update := <-updates:
			if update.Operation == agent.ProgressOutputTail {
				assert.Equal(t, "after retry", update.Text)
				return
			}
		case <-ctx.Done():
			t.Fatal("the native shell did not recover after the read failure")
		}
	}
}

func TestNativeShellOutputStopsAnInflightReadWhenTheToolCloses(t *testing.T) {
	t.Parallel()
	reading := make(chan struct{})
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/shell/sh_1" {
			_, _ = w.Write([]byte(`{"data":{"id":"sh_1","metadata":{"sessionID":"ses_1"}}}`))
			return
		}
		close(reading)
		<-r.Context().Done()
	})
	a, sink := assemblyAgent(t, rpc)
	beginNativeShellTestTool(t, a)
	select {
	case <-reading:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native shell output read did not start")
	}
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.tool.success", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_shell", "id": "call_shell", "executed": false,
		"content": []map[string]any{{"type": "text", "text": "finished"}},
	})))
	waitNativeShellReaders(t, a)
	for _, update := range sink.ProgressUpdates() {
		assert.NotEqual(t, agent.ProgressOutputTail, update.Operation)
	}
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress(openCodeToolSpanID("msg_shell", "call_shell")))
}

func TestNativeShellOutputCapsTheLiveTail(t *testing.T) {
	t.Parallel()
	output := strings.Repeat("x", openCodeShellTailSize+100)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/shell/sh_1" {
			_, _ = w.Write([]byte(`{"data":{"id":"sh_1","metadata":{"sessionID":"ses_1"}}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"output": output, "cursor": len(output), "size": len(output), "truncated": false}})
	})
	a, _ := assemblyAgent(t, rpc)
	updates := beginNativeShellTestTool(t, a)
	ctx := testutil.DeadlineContext(t)
	for {
		select {
		case update := <-updates:
			if update.Operation != agent.ProgressOutputTail {
				continue
			}
			assert.Len(t, update.Text, openCodeShellTailSize)
			assert.True(t, update.Truncated)
			return
		case <-ctx.Done():
			t.Fatal("the native shell produced no live tail")
		}
	}
}

type shellProgressCapture struct {
	agent.ProviderServices
	updates chan agent.ProgressUpdate
}

func (s shellProgressCapture) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	content = s.ProviderServices.CaptureMessage(content, span)
	content.Publication = shellProgressPublication{TranscriptPublication: content.Publication, updates: s.updates}
	return content
}

type shellProgressPublication struct {
	agent.TranscriptPublication
	updates chan agent.ProgressUpdate
}

func (p shellProgressPublication) ReportProgress(update agent.ProgressUpdate) {
	p.TranscriptPublication.ReportProgress(update)
	select {
	case p.updates <- update:
	default:
	}
}

func beginNativeShellTestTool(t *testing.T, a *Agent) chan agent.ProgressUpdate {
	t.Helper()
	a.root.location.Directory = "/project"
	updates := make(chan agent.ProgressUpdate, 64)
	a.root.sink = shellProgressCapture{ProviderServices: a.root.sink, updates: updates}
	a.root.setNativeActivity(true, false)
	for _, frame := range []struct {
		kind   string
		fields map[string]any
	}{
		{"session.tool.input.started", map[string]any{"name": "shell"}},
		{"session.tool.called", map[string]any{"input": map[string]any{"command": "printf hello"}}},
		{"session.tool.progress", map[string]any{"metadata": map[string]any{"shellID": "sh_1"}}},
	} {
		data := map[string]any{"sessionID": "ses_1", "assistantMessageID": "msg_shell", "id": "call_shell", "executed": false}
		for key, value := range frame.fields {
			data[key] = value
		}
		require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, frame.kind, data)))
	}
	return updates
}

func waitNativeShellReaders(t *testing.T, a *Agent) {
	t.Helper()
	done := make(chan struct{})
	go func() { a.shellWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native shell readers did not finish")
	}
}

func TestNativeShellOutputIsVisibleBeforeToolCompletion(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/shell/sh_1":
			assert.Equal(t, "/project", r.URL.Query().Get("location[directory]"))
			_, _ = w.Write([]byte(`{"data":{"id":"sh_1","status":"running","file":"/native/sh_1.out","metadata":{"sessionID":"ses_1"}}}`))
		case "/api/shell/sh_1/output":
			if r.URL.Query().Get("cursor") == "0" {
				_, _ = w.Write([]byte(`{"data":{"output":"hello","cursor":5,"size":5,"truncated":false}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"output":"","cursor":5,"size":5,"truncated":false}}`))
			}
		default:
			t.Errorf("unexpected native shell request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	updates := beginNativeShellTestTool(t, a)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		select {
		case update := <-updates:
			if update.Operation != agent.ProgressOutputTail || update.Text != "hello" {
				continue
			}
			assert.Equal(t, openCodeToolSpanID("msg_shell", "call_shell"), update.ScopeID)
			for _, row := range sink.Messages() {
				assert.False(t, row.Closing, "the output must precede the completed result")
			}
			return
		case <-ctx.Done():
			t.Fatal("the native shell produced no live output before completion")
		}
	}
}

func TestNativeShellOutputRequiresCompletePageFields(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"data":{}}`,
		`{"data":{"output":"","size":0,"truncated":false}}`,
		`{"data":{"output":"","cursor":0,"truncated":false}}`,
		`{"data":{"output":"","cursor":0,"size":0}}`,
		`{"data":{"output":"","cursor":-1,"size":0,"truncated":false}}`,
	} {
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		_, err := rpc.shellOutput(t.Context(), "sh_1", openCodeLocation{}, 0)
		require.Error(t, err, body)
	}
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"output":"","cursor":0,"size":0,"truncated":false}}`))
	})
	page, err := rpc.shellOutput(t.Context(), "sh_1", openCodeLocation{}, 0)
	require.NoError(t, err)
	assert.Zero(t, page.Cursor)
	assert.Zero(t, page.Size)
	assert.False(t, page.Truncated)
}

func TestNativeShellCompletionClearsProgressAfterReaderExit(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	tool := &openCodeToolState{name: "shell", spanID: "saved-shell", closed: true}
	a.root.sink.ReportProgress(agent.OutputExactTotalProgress(tool.spanID, 5))
	a.stopNativeShellReader(a.root, tool)
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteOutputProgress(tool.spanID))
}
