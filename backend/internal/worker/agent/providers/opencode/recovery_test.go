package opencode

import (
	"net/http"
	"testing"

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
