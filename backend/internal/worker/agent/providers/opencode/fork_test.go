package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestOpenCodeRPCForkSession(t *testing.T) {
	t.Parallel()

	t.Run("posts to /api/session/<id>/fork and adopts new session ID", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			called.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/session/ses_source/fork", r.URL.Path)
			_, _ = w.Write([]byte(`{"data":{"id":"ses_forked","agent":"build","model":{"providerID":"anthropic","id":"claude-sonnet-4"},"location":{"directory":"/work"}}}`))
		})

		info, err := rpc.forkSession(context.Background(), "ses_source")
		require.NoError(t, err)
		assert.EqualValues(t, 1, called.Load())
		assert.Equal(t, "ses_forked", info.ID)
		assert.Equal(t, "build", info.Agent)
		require.NotNil(t, info.Model)
		assert.Equal(t, "anthropic", info.Model.ProviderID)
		assert.Equal(t, "claude-sonnet-4", info.Model.ID)
	})

	t.Run("rejects invalid source session ID without sending a request", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
			called.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		_, err := rpc.forkSession(context.Background(), "../escape")
		require.Error(t, err)
		assert.Zero(t, called.Load())
	})

	t.Run("rejects fork reply returning source ID or empty ID", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{
			`{"data":{"id":"ses_source"}}`,
			`{"data":{"id":""}}`,
			`{"data":null}`,
		} {
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			_, err := rpc.forkSession(context.Background(), "ses_source")
			require.Error(t, err, body)
		}
	})

	t.Run("moveSession posts target directory to /api/session/<id>/move and accepts 204", func(t *testing.T) {
		t.Parallel()
		var gotBody map[string]any
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/session/ses_forked/move", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusNoContent)
		})
		require.NoError(t, rpc.moveSession(context.Background(), "ses_forked", "/target/worktree"))
		assert.Equal(t, map[string]any{"directory": "/target/worktree"}, gotBody)
	})

	t.Run("moveSession rejects invalid session ID or empty directory without sending a request", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
			called.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
		require.Error(t, rpc.moveSession(context.Background(), "../escape", "/target"))
		require.Error(t, rpc.moveSession(context.Background(), "ses_forked", "   "))
		assert.Zero(t, called.Load())
	})

	t.Run("openRootSession skips /move on same-directory fork", func(t *testing.T) {
		t.Parallel()
		var forkCalls, moveCalls atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /api/session/ses_orig/fork":
				forkCalls.Add(1)
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/work"}}}`))
			case "POST /api/session/ses_new/move":
				moveCalls.Add(1)
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		a := &Agent{rpc: rpc}
		info, err := a.openRootSession(context.Background(), agent.Options{
			WorkingDir: "/work/",
			Session:    agent.ForkSession("ses_orig", ""),
		})
		require.NoError(t, err)
		assert.Equal(t, "ses_new", info.ID)
		assert.Equal(t, "/work", info.Location.Directory)
		assert.EqualValues(t, 1, forkCalls.Load())
		assert.Zero(t, moveCalls.Load(), "same-directory fork must skip /move")
	})

	t.Run("openRootSession moves forked session on cross-directory fork and waits for settlement", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		trap := clock.Trap().NewTimer(openCodeMovePollTag)
		t.Cleanup(trap.Close)

		var (
			moveCalls   atomic.Int32
			getCalls    atomic.Int32
			activeCalls atomic.Int32
			movedDir    atomic.Value
		)
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /api/session/ses_orig/fork":
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/source/repo"}}}`))
			case "POST /api/session/ses_new/move":
				moveCalls.Add(1)
				var body struct {
					Directory string `json:"directory"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				movedDir.Store(body.Directory)
				w.WriteHeader(http.StatusNoContent)
			case "GET /api/session/ses_new":
				// First poll still reports the pre-move directory; subsequent polls report the target.
				if getCalls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/source/repo"}}}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/target/worktree"}}}`))
			case "GET /api/session/active":
				// First active check reports the move inbox execution still running; second reports idle.
				if activeCalls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"data":{"ses_new":{"type":"running"}}}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{}}`))
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})

		a := &Agent{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Clock: clock}),
			rpc:     rpc,
		}
		type openResult struct {
			info openCodeSessionInfo
			err  error
		}
		done := make(chan openResult, 1)
		go func() {
			info, err := a.openRootSession(ctx, agent.Options{
				WorkingDir:     "/target/worktree/",
				Session:        agent.ForkSession("ses_orig", "/source/repo"),
				StartupTimeout: 10 * time.Second,
			})
			done <- openResult{info, err}
		}()

		// Two backoff waits: once for location.directory to update, once for active execution to settle.
		delay1 := testutil.WaitForTimer(t, ctx, trap)
		assert.Equal(t, openCodeMoveInitialBackoff, delay1)
		clock.Advance(delay1).MustWait(ctx)

		delay2 := testutil.WaitForTimer(t, ctx, trap)
		assert.Equal(t, openCodeMoveInitialBackoff*2, delay2)
		clock.Advance(delay2).MustWait(ctx)

		select {
		case res := <-done:
			require.NoError(t, res.err)
			assert.Equal(t, "ses_new", res.info.ID)
			assert.Equal(t, "/target/worktree", res.info.Location.Directory)
		case <-ctx.Done():
			t.Fatal("openRootSession did not finish after move settled")
		}
		assert.EqualValues(t, 1, moveCalls.Load())
		assert.Equal(t, "/target/worktree", movedDir.Load())
		assert.EqualValues(t, 3, getCalls.Load())
		assert.EqualValues(t, 2, activeCalls.Load())
	})

	t.Run("openRootSession fails fork startup cleanly when /move returns HTTP error", func(t *testing.T) {
		t.Parallel()
		var getCalls atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /api/session/ses_orig/fork":
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/source/repo"}}}`))
			case "POST /api/session/ses_new/move":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"_tag":"InvalidRequestError","message":"Directory does not exist: /missing"}`))
			case "GET /api/session/ses_new":
				getCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		a := &Agent{rpc: rpc}
		_, err := a.openRootSession(context.Background(), agent.Options{
			WorkingDir: "/missing",
			Session:    agent.ForkSession("ses_orig", "/source/repo"),
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "Directory does not exist")
		var statusErr *providerkit.HTTPStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusBadRequest, statusErr.StatusCode)
		assert.Zero(t, getCalls.Load(), "must not poll session location after /move HTTP error")
	})

	t.Run("openRootSession times out cleanly if moved location never updates", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		trap := clock.Trap().NewTimer(openCodeMovePollTag)
		t.Cleanup(trap.Close)

		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /api/session/ses_orig/fork":
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/source/repo"}}}`))
			case "POST /api/session/ses_new/move":
				w.WriteHeader(http.StatusNoContent)
			case "GET /api/session/ses_new":
				_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/source/repo"}}}`))
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})

		a := &Agent{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Clock: clock}),
			rpc:     rpc,
		}
		const moveTimeout = 2 * time.Second
		done := make(chan struct{})
		var gotErr error
		go func() {
			defer close(done)
			_, gotErr = a.openRootSession(ctx, agent.Options{
				WorkingDir:     "/target/worktree",
				Session:        agent.ForkSession("ses_orig", "/source/repo"),
				StartupTimeout: moveTimeout,
			})
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.Error(t, gotErr)
		assert.ErrorContains(t, gotErr, "timed out waiting for OpenCode session")
		assert.Equal(t, moveTimeout, clock.Now().Sub(start))
	})

	t.Run("openRootSession rejects invalid SessionInit", func(t *testing.T) {
		t.Parallel()
		a := &Agent{}
		_, err := a.openRootSession(context.Background(), agent.Options{
			Session: agent.SessionInit{
				Kind:             agent.SessionInitResume,
				SessionID:        "ses_a",
				SourceWorkingDir: "/source",
			},
		})
		require.Error(t, err)
	})
}

func TestOpenCodeForkSSEReconnectDoesNotDuplicatePreforkHistory(t *testing.T) {
	t.Parallel()
	var includePostFork atomic.Bool
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model/default":
			_, _ = w.Write([]byte(`{"data":{"id":"model","providerID":"provider"}}`))
		case "/api/model":
			_, _ = w.Write([]byte(`{"data":` + openCodeCatalogModelsFixture + `}`))
		case "/api/agent":
			_, _ = w.Write([]byte(`{"data":` + openCodeCatalogAgentsFixture + `}`))
		case "/api/session/ses_1":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_1","agent":"plan","model":{"providerID":"provider","id":"model"}}}`))
		case "/api/session/ses_1/message":
			msgs := `{"id":"msg_pre_u1","type":"user","content":[{"type":"text","text":"pre-fork question"}]},` +
				`{"id":"msg_pre_a1","type":"assistant","content":[{"type":"tool","id":"call_pre","name":"read","state":{"status":"completed","input":{"path":"a.go"},"content":[{"type":"text","text":"ok"}]}},{"type":"text","text":"pre-fork answer"}],"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}},"model":{"providerID":"provider","id":"model"},"time":{"completed":1}},` +
				`{"id":"msg_pre_idle1","type":"idle","outcome":"succeeded"}`
			if includePostFork.Load() {
				msgs += `,{"id":"msg_post_a1","type":"assistant","content":[{"type":"text","text":"post-fork answer"}],"time":{"completed":2}},` +
					`{"id":"msg_post_idle1","type":"idle","outcome":"succeeded"}`
			}
			_, _ = w.Write([]byte(`{"data":[` + msgs + `],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/session/ses_1/permission", "/api/session/ses_1/form", "/api/session/global/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	// Fork startup seeds baseline message IDs and syncs session metadata/usage without persisting pre-fork rows.
	a.dispatch.Lock()
	err := a.restoreForkBaselineLocked(t.Context(), a.root)
	a.dispatch.Unlock()
	require.NoError(t, err)
	assert.Empty(t, sink.Messages(), "fork baseline restore must not persist pre-fork rows")
	assert.Equal(t, "plan", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPrimaryAgent], "fork baseline restore must sync session info")
	usageVal, found := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, found, "fork baseline restore must publish native usage from pre-fork history")
	assert.NotNil(t, usageVal)
	assert.True(t, a.root.ended["msg_pre_idle1"], "fork baseline restore must mark pre-fork idle messages ended")

	// Simulate SSE reconnect after a post-fork turn completes.
	includePostFork.Store(true)
	require.NoError(t, a.restoreStream(t.Context()))
	rows := sink.Messages()
	require.Len(t, rows, 2, "SSE reconnect after fork must only persist post-fork rows, not duplicate pre-fork history")
	assert.Contains(t, string(rows[0].Content), "post-fork answer")
	assert.Contains(t, string(rows[1].Content), `"id":"msg_post_idle1"`)

	// A second SSE reconnect must remain idempotent for both pre-fork and post-fork rows.
	require.NoError(t, a.restoreStream(t.Context()))
	assert.Len(t, sink.Messages(), 2)
}

func TestOpenCodeForkWorkerRestartDoesNotDuplicatePreforkHistory(t *testing.T) {
	t.Parallel()
	var includePostFork atomic.Bool
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model/default":
			_, _ = w.Write([]byte(`{"data":{"id":"model","providerID":"provider"}}`))
		case "/api/model":
			_, _ = w.Write([]byte(`{"data":` + openCodeCatalogModelsFixture + `}`))
		case "/api/agent":
			_, _ = w.Write([]byte(`{"data":` + openCodeCatalogAgentsFixture + `}`))
		case "/api/session/ses_1":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_1","agent":"plan","model":{"providerID":"provider","id":"model"}}}`))
		case "/api/session/ses_1/message":
			msgs := `{"id":"msg_pre_u1","type":"user","content":[{"type":"text","text":"pre-fork question"}]},` +
				`{"id":"msg_pre_a1","type":"assistant","content":[{"type":"tool","id":"call_pre","name":"read","state":{"status":"completed","input":{"path":"a.go"},"content":[{"type":"text","text":"ok"}]}},{"type":"text","text":"pre-fork answer"}],"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}},"model":{"providerID":"provider","id":"model"},"time":{"completed":1}},` +
				`{"id":"msg_pre_idle1","type":"idle","outcome":"succeeded"}`
			if includePostFork.Load() {
				msgs += `,{"id":"msg_post_a1","type":"assistant","content":[{"type":"text","text":"post-fork answer"}],"time":{"completed":2}},` +
					`{"id":"msg_post_idle1","type":"idle","outcome":"succeeded"}`
			}
			_, _ = w.Write([]byte(`{"data":[` + msgs + `],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/session/ses_1/permission", "/api/session/ses_1/form", "/api/session/global/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a1, sink := assemblyAgent(t, rpc)
	a1.dispatch.Lock()
	err := a1.restoreForkBaselineLocked(t.Context(), a1.root)
	a1.dispatch.Unlock()
	require.NoError(t, err)
	assert.Empty(t, sink.Messages())

	// Simulate a Worker restart: a fresh *Agent with empty in-memory suppressedBaselineIDs
	// sharing the same durable sink restarts and resumes the forked session via restoreRun.
	ctx2, cancel2 := context.WithCancel(t.Context())
	services2 := agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a2 := &Agent{
		Process:      providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: ctx2, Cancel: cancel2}),
		sink:         services2,
		rpc:          rpc,
		catalog:      openCodeCatalogFixture(t),
		runs:         make(map[string]*openCodeRun),
		nativeCancel: cancel2,
	}
	controls2, err := newOpenCodeControls(ctx2, rpc, a2.requestRecovery)
	require.NoError(t, err)
	a2.controls = controls2
	run2, err := a2.newRun(openCodeSessionInfo{ID: "ses_1", Agent: "build"}, services2)
	require.NoError(t, err)
	a2.root = run2
	a2.runs[run2.id] = run2
	services2.AdoptNativeSession(run2.id)
	t.Cleanup(func() {
		cancel2()
		a2.closeNativeShellReaders()
		controls2.close()
		a2.dispatch.Lock()
		defer a2.dispatch.Unlock()
		assert.NoError(t, a2.finishNativeOutputs(agent.MessageCompletionInterrupted))
	})

	includePostFork.Store(true)
	a2.dispatch.Lock()
	err = a2.restoreRunLocked(t.Context(), a2.root)
	a2.dispatch.Unlock()
	require.NoError(t, err)

	rows := sink.Messages()
	require.Len(t, rows, 2, "Worker restart after fork must not duplicate pre-fork history")
	assert.Contains(t, string(rows[0].Content), "post-fork answer")
	assert.Contains(t, string(rows[1].Content), `"id":"msg_post_idle1"`)
	usageVal, found := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, found)
	assert.NotNil(t, usageVal)
}
