package antigravity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityFailedChildStreamCanReopenAndStop(t *testing.T) {
	var failed atomic.Bool
	delivered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerService + "StreamAgentStateUpdates":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) || !assert.GreaterOrEqual(t, len(body), 5) {
				return
			}
			var request struct {
				InitialStepsPageBounds struct {
					StartIndex int `json:"startIndex"`
				} `json:"initialStepsPageBounds"`
			}
			if !assert.NoError(t, json.Unmarshal(body[5:], &request)) {
				return
			}
			if request.InitialStepsPageBounds.StartIndex == pastEveryStep {
				_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"mainTrajectoryUpdate":{"stepsUpdate":{"totalLength":0}}}}`)))
				return
			}
			if failed.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case languageServerService + "CancelCascadeInvocation":
			_, _ = w.Write([]byte(`{}`))
		case languageServerService + "SendUserCascadeMessage":
			var request struct {
				CascadeID string `json:"cascadeId"`
			}
			if assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				delivered <- request.CascadeID
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected native method: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a, sink := testAgent(t)
	a.rpc = antigravityRPC{endpoint: endpoint, timeout: 30 * time.Second}
	defer a.streamCancel()
	child := newCascadeState("child", a.sink.ChildSink("child-agent"), a.generation, false)
	child.parent = a.root
	child.childAgentID = "child-agent"
	child.rowOpen = true
	a.cascades[child.id] = child
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: child.id, ChildAgentID: child.childAgentID, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}))
	a.startCascadeStream(child)
	a.streamWG.Wait()
	require.True(t, child.closed)
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)

	require.NoError(t, a.SendChildInput(child.id, "continue the child", nil))
	assert.Equal(t, child.id, <-delivered)
	a.streamCancel()
	a.streamWG.Wait()
}
