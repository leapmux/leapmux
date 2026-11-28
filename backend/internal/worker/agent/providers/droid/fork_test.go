package droid

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDroidProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func readNextFrame(t *testing.T, stdin *bufferStdin, wantCount int) droidEnvelope {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		frames := stdin.frames()
		if len(frames) >= wantCount {
			var req droidEnvelope
			require.NoError(t, json.Unmarshal([]byte(frames[wantCount-1]), &req))
			return req
		}
		select {
		case <-stdin.written:
		case <-deadline:
			t.Fatalf("timed out waiting for frame %d (got %d)", wantCount, len(stdin.frames()))
		}
	}
}

func TestHandshakeForksAnExistingSession(t *testing.T) {
	t.Parallel()

	t.Run("loads source session, forks it, and loads the new session ID", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newSteerAgent(t)
		options := agent.Options{ForkFromSessionID: "source-session", WorkingDir: "/work/tree"}
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(options) }()

		// 1. Load source session.
		loadReq := readNextFrame(t, stdin, 1)
		assert.Equal(t, droidMethodLoadSession, loadReq.Method)
		var loadParams map[string]any
		require.NoError(t, json.Unmarshal(loadReq.Params, &loadParams))
		assert.Equal(t, map[string]any{"sessionId": "source-session"}, loadParams)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session","settings":{},"availableModels":[]}}`))
		assert.Empty(t, sink.SessionIDs(), "loading the source session before fork must not emit the source session ID")

		// 2. Fork active session.
		forkReq := readNextFrame(t, stdin, 2)
		assert.Equal(t, droidMethodForkSession, forkReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"forked-session"}}`))

		// 3. Load newly forked session and adopt settings/session ID.
		initReq := readNextFrame(t, stdin, 3)
		assert.Equal(t, droidMethodLoadSession, initReq.Method)
		assert.Equal(t, droidInitRequestID, initReq.ID)
		var initParams map[string]any
		require.NoError(t, json.Unmarshal(initReq.Params, &initParams))
		assert.Equal(t, map[string]any{"sessionId": "forked-session"}, initParams)
		a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"forked-session","settings":{"modelId":"custom:Droid-1"},"availableModels":[]}}`))

		select {
		case err := <-returned:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the native fork handshake did not settle")
		}

		a.Mu.Lock()
		sessionID, model := a.sessionID, a.settings.model
		a.Mu.Unlock()
		assert.Equal(t, "forked-session", sessionID)
		assert.Equal(t, "custom:Droid-1", model)
		assert.Equal(t, []string{"forked-session"}, sink.SessionIDs())
	})

	t.Run("reports native fork RPC error", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(agent.Options{ForkFromSessionID: "source-session"}) }()

		loadReq := readNextFrame(t, stdin, 1)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		forkReq := readNextFrame(t, stdin, 2)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","error":{"code":-32000,"message":"session not found"}}`))

		select {
		case err := <-returned:
			require.ErrorContains(t, err, "session not found")
		case <-time.After(30 * time.Second):
			t.Fatal("the native error did not settle the handshake")
		}
	})

	t.Run("rejects fork response returning the source session ID", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(agent.Options{ForkFromSessionID: "source-session"}) }()

		loadReq := readNextFrame(t, stdin, 1)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		forkReq := readNextFrame(t, stdin, 2)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"source-session"}}`))

		select {
		case err := <-returned:
			require.ErrorContains(t, err, "source session ID")
		case <-time.After(30 * time.Second):
			t.Fatal("the native response did not settle the handshake")
		}
	})

	t.Run("rejects simultaneous resume and fork", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newSteerAgent(t)
		err := a.handshake(agent.Options{ResumeSessionID: "s1", ForkFromSessionID: "s2"})
		require.Error(t, err)
	})

	t.Run("aborts before fork when step-1 load_session fails", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(agent.Options{ForkFromSessionID: "source-session"}) }()

		loadReq := readNextFrame(t, stdin, 1)
		assert.Equal(t, droidMethodLoadSession, loadReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","error":{"code":-32000,"message":"source session missing"}}`))

		select {
		case err := <-returned:
			require.ErrorContains(t, err, "source session missing")
		case <-time.After(30 * time.Second):
			t.Fatal("the step-1 load error did not settle the handshake")
		}
		assert.Len(t, stdin.frames(), 1, "droid.fork_session must not be sent after load_session fails")
	})

	t.Run("fails when fork_session returns empty object", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(agent.Options{ForkFromSessionID: "source-session"}) }()

		loadReq := readNextFrame(t, stdin, 1)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		forkReq := readNextFrame(t, stdin, 2)
		assert.Equal(t, droidMethodForkSession, forkReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{}}`))

		select {
		case err := <-returned:
			require.ErrorContains(t, err, "no new session id")
		case <-time.After(30 * time.Second):
			t.Fatal("the empty fork response did not settle the handshake")
		}
		assert.Len(t, stdin.frames(), 2, "must not load a session when fork_session returned no session ID")
	})

	for _, tc := range []struct {
		name       string
		forkResult string
		wantID     string
	}{
		{
			name:       "falls back to sessionId when newSessionId is omitted",
			forkResult: `{"sessionId":"forked-via-session-id"}`,
			wantID:     "forked-via-session-id",
		},
		{
			name:       "falls back to nested session.id when newSessionId and sessionId are omitted",
			forkResult: `{"session":{"id":"forked-via-nested-id"}}`,
			wantID:     "forked-via-nested-id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newSteerAgent(t)
			returned := make(chan error, 1)
			go func() { returned <- a.handshake(agent.Options{ForkFromSessionID: "source-session"}) }()

			loadReq := readNextFrame(t, stdin, 1)
			a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

			forkReq := readNextFrame(t, stdin, 2)
			a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":` + tc.forkResult + `}`))

			initReq := readNextFrame(t, stdin, 3)
			assert.Equal(t, droidMethodLoadSession, initReq.Method)
			assert.Equal(t, droidInitRequestID, initReq.ID)
			var initParams map[string]any
			require.NoError(t, json.Unmarshal(initReq.Params, &initParams))
			assert.Equal(t, map[string]any{"sessionId": tc.wantID}, initParams)
			a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"` + tc.wantID + `","settings":{},"availableModels":[]}}`))

			select {
			case err := <-returned:
				require.NoError(t, err)
			case <-time.After(30 * time.Second):
				t.Fatal("the fallback fork handshake did not settle")
			}

			a.Mu.Lock()
			gotSessionID := a.sessionID
			a.Mu.Unlock()
			assert.Equal(t, tc.wantID, gotSessionID)
			assert.Equal(t, []string{tc.wantID}, sink.SessionIDs())
		})
	}
}
