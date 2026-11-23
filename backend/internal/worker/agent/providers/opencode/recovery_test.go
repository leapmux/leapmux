package opencode

import (
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeRecoveryKeepsCompletionBeforeTheNextTurn(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_1":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_1","agent":"build"}}`))
		case "/api/session/ses_1/message":
			_, _ = w.Write([]byte(`{"data":[{"id":"msg_a1","type":"assistant","content":[{"type":"text","text":"first answer"}],"time":{"completed":1}},{"id":"msg_end1","type":"idle","outcome":"succeeded"},{"id":"msg_a2","type":"assistant","content":[{"type":"text","text":"second answer"}],"time":{"completed":2}},{"id":"msg_end2","type":"idle","outcome":"succeeded"}],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/session/ses_1/permission", "/api/session/ses_1/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected recovery request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.restoreRun(t.Context(), a.root))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[0].Content), "first answer")
	assert.Contains(t, string(rows[1].Content), `"id":"msg_end1"`)
	assert.Contains(t, string(rows[2].Content), "second answer")
	assert.Contains(t, string(rows[3].Content), `"id":"msg_end2"`)
	require.NoError(t, a.restoreRun(t.Context(), a.root))
	assert.Len(t, sink.Messages(), 4, "reconnection must not duplicate historical rows")
}

func TestNativeChildRecoveryInactiveWithoutIdleClosesUnknownOutcome(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_child":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_child","parentID":"ses_1","agent":"general"}}`))
		case "/api/session/ses_child/message":
			_, _ = w.Write([]byte(`{"data":[],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/session/ses_child/permission", "/api/session/ses_child/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected child recovery request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)

	require.NoError(t, a.restoreRun(t.Context(), run))
	item, ok := sink.BackgroundTask("ses_child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, item.Status)
}

func TestNativeChildRecovery404RetiresAndClosesRow(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/ses_child" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("unexpected child recovery request: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	a, sink := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)

	require.NoError(t, a.restoreRun(t.Context(), run))
	item, ok := sink.BackgroundTask("ses_child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusInterrupted, item.Status)
	a.Mu.Lock()
	_, exists := a.runs["ses_child"]
	a.Mu.Unlock()
	assert.False(t, exists, "404 child must be removed from runs")
}

func TestNativeChildRecoveryActiveRevivesFinishedRow(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_child":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_child","parentID":"ses_1","agent":"general"}}`))
		case "/api/session/ses_child/message":
			_, _ = w.Write([]byte(`{"data":[],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{"ses_child":{"type":"running"}}}`))
		case "/api/session/ses_child/permission", "/api/session/ses_child/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected child recovery request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)

	ended := assemblyEvent(t, "session.execution.succeeded", map[string]any{"sessionID": "ses_child"})
	require.NoError(t, a.acceptNativeEvent(t.Context(), ended))
	item, ok := sink.BackgroundTask("ses_child")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, item.Status)

	require.NoError(t, a.restoreRun(t.Context(), run))
	assert.Equal(t, []string{"ses_child"}, sink.RevivedTasks())
	item, ok = sink.BackgroundTask("ses_child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, item.Status)
}
