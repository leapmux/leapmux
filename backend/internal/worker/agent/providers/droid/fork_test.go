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
		options := agent.Options{Session: agent.ForkSession("source-session", ""), WorkingDir: "/work/tree"}
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
		go func() { returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", "")}) }()

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
		go func() { returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", "")}) }()

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

	t.Run("rejects invalid session init", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newSteerAgent(t)
		err := a.handshake(agent.Options{
			Session: agent.SessionInit{
				Kind:             agent.SessionInitResume,
				SessionID:        "s1",
				SourceWorkingDir: "/source",
			},
		})
		require.Error(t, err)
	})

	t.Run("aborts before fork when step-1 load_session fails", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() { returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", "")}) }()

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
		go func() { returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", "")}) }()

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
			go func() { returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", "")}) }()

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

	t.Run("suppresses replayed notifications during fork handshake and delivers live notifications afterward", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() {
			returned <- a.handshake(agent.Options{Session: agent.ForkSession("source-session", ""), WorkingDir: "/work/tree"})
		}()

		// 1. Load source session; replay notifications arrive before the reply.
		loadReq := readNextFrame(t, stdin, 1)
		assert.Equal(t, droidMethodLoadSession, loadReq.Method)
		a.HandleOutput([]byte(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"","notification":{"type":"create_message","message":{"id":"m-step1","role":"assistant","content":[{"type":"text","text":"REPLAY_STEP_1"}]}}}}`))
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		// 2. Fork active session; replay notifications arrive before the reply.
		forkReq := readNextFrame(t, stdin, 2)
		assert.Equal(t, droidMethodForkSession, forkReq.Method)
		a.HandleOutput([]byte(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"","notification":{"type":"create_message","message":{"id":"m-step2","role":"assistant","content":[{"type":"text","text":"REPLAY_STEP_2"}]}}}}`))
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"forked-session"}}`))

		// 3. Load forked session; replay notifications carrying forked-session arrive before leapmux-init reply.
		initReq := readNextFrame(t, stdin, 3)
		assert.Equal(t, droidMethodLoadSession, initReq.Method)
		assert.Equal(t, droidInitRequestID, initReq.ID)
		a.HandleOutput([]byte(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"forked-session","notification":{"type":"create_message","message":{"id":"m-step3","role":"assistant","content":[{"type":"text","text":"REPLAY_STEP_3"}]}}}}`))
		a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"forked-session","settings":{},"availableModels":[]}}`))

		select {
		case err := <-returned:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the native fork handshake did not settle")
		}

		assert.Empty(t, sink.Messages(), "notifications replayed during fork handshake must not reach the sink")

		// Live notification after handshake must be delivered normally.
		a.HandleOutput([]byte(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"forked-session","notification":{"type":"create_message","message":{"id":"m-live","role":"assistant","content":[{"type":"text","text":"LIVE_AFTER_FORK"}]}}}}`))
		msgs := sink.Messages()
		require.NotEmpty(t, msgs, "live notifications after fork handshake must be persisted")
		var combined string
		for _, m := range msgs {
			combined += string(m.Content)
		}
		assert.Contains(t, combined, "LIVE_AFTER_FORK")
		assert.NotContains(t, combined, "REPLAY_STEP_")
	})

	t.Run("switches working directory after cross-directory fork", func(t *testing.T) {
		t.Parallel()
		a, sink, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() {
			returned <- a.handshake(agent.Options{
				Session:    agent.ForkSession("source-session", "/source/repo"),
				WorkingDir: "/work/tree",
			})
		}()

		loadReq := readNextFrame(t, stdin, 1)
		assert.Equal(t, droidMethodLoadSession, loadReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		forkReq := readNextFrame(t, stdin, 2)
		assert.Equal(t, droidMethodForkSession, forkReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"forked-session"}}`))

		initReq := readNextFrame(t, stdin, 3)
		assert.Equal(t, droidMethodLoadSession, initReq.Method)
		assert.Equal(t, droidInitRequestID, initReq.ID)
		a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"forked-session","settings":{},"availableModels":[]}}`))

		cwdReq := readNextFrame(t, stdin, 4)
		assert.Equal(t, droidMethodChangeWorkingDirectory, cwdReq.Method)
		var cwdParams map[string]any
		require.NoError(t, json.Unmarshal(cwdReq.Params, &cwdParams))
		assert.Equal(t, map[string]any{"workingDirectory": "/work/tree"}, cwdParams)
		a.HandleOutput([]byte(`{"type":"response","id":"` + cwdReq.ID + `","result":{"resolvedPath":"/work/tree"}}`))

		select {
		case err := <-returned:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the cross-directory fork handshake did not settle")
		}
		assert.Equal(t, []string{"forked-session"}, sink.SessionIDs())
	})

	t.Run("fails handshake when change_working_directory returns an RPC error", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() {
			returned <- a.handshake(agent.Options{
				Session:    agent.ForkSession("source-session", "/source/repo"),
				WorkingDir: "/work/tree",
			})
		}()

		loadReq := readNextFrame(t, stdin, 1)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))

		forkReq := readNextFrame(t, stdin, 2)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"forked-session"}}`))

		_ = readNextFrame(t, stdin, 3)
		a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"forked-session","settings":{},"availableModels":[]}}`))

		cwdReq := readNextFrame(t, stdin, 4)
		assert.Equal(t, droidMethodChangeWorkingDirectory, cwdReq.Method)
		a.HandleOutput([]byte(`{"type":"response","id":"` + cwdReq.ID + `","error":{"code":-32600,"message":"Folder is not trusted"}}`))

		select {
		case err := <-returned:
			require.ErrorContains(t, err, "Folder is not trusted")
		case <-time.After(30 * time.Second):
			t.Fatal("the change_working_directory error did not settle the handshake")
		}
	})

	t.Run("skips change_working_directory when source and target working dirs match", func(t *testing.T) {
		t.Parallel()
		a, _, stdin := newSteerAgent(t)
		returned := make(chan error, 1)
		go func() {
			returned <- a.handshake(agent.Options{
				Session:    agent.ForkSession("source-session", "/work/tree"),
				WorkingDir: "/work/tree",
			})
		}()

		loadReq := readNextFrame(t, stdin, 1)
		a.HandleOutput([]byte(`{"type":"response","id":"` + loadReq.ID + `","result":{"sessionId":"source-session"}}`))
		forkReq := readNextFrame(t, stdin, 2)
		a.HandleOutput([]byte(`{"type":"response","id":"` + forkReq.ID + `","result":{"newSessionId":"forked-session"}}`))
		_ = readNextFrame(t, stdin, 3)
		a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"forked-session","settings":{},"availableModels":[]}}`))

		select {
		case err := <-returned:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the same-directory fork handshake did not settle")
		}
		assert.Len(t, stdin.frames(), 3, "must not send droid.change_working_directory when directories match")
	})
}

func TestDroidInitialCwd(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "/work/tree", droidInitialCwd(agent.Options{WorkingDir: "/work/tree"}))
	assert.Equal(t, "/work/tree", droidInitialCwd(agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("src", "")}))
	assert.Equal(t, "/source/repo", droidInitialCwd(agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("src", "/source/repo")}))
	assert.Equal(t, "/work/tree", droidInitialCwd(agent.Options{WorkingDir: "/work/tree", Session: agent.ResumeSession("res")}))
	assert.Equal(t, "/work/tree", droidInitialCwd(agent.Options{WorkingDir: "/work/tree", Session: agent.SessionInit{Kind: agent.SessionInitResume, SessionID: "res", SourceWorkingDir: "/source/repo"}}))
}
