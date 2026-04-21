package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// TestOpenAgent_SyncPrologueReturnsFast asserts that even when startAgent
// blocks for seconds, the OpenAgent RPC response lands in the test writer
// within ~200 ms — the whole point of the OpenAgent split.
func TestOpenAgent_SyncPrologueReturnsFast(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	released := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		select {
		case <-released:
		case <-ctx.Done():
		}
		return &leapmuxv1.AgentSettings{}, nil
	}
	defer close(released)

	start := time.Now()
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	syncLatency := time.Since(start)

	assert.Less(t, syncLatency, 200*time.Millisecond,
		"sync prologue must return well under 200 ms even when startAgent blocks; got %v", syncLatency)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotNil(t, resp.GetAgent())
	// Initial response carries STARTING — runtime fields (session_id,
	// available_models) are filled in by the subsequent AgentStatusChange.
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, resp.GetAgent().GetStatus(),
		"OpenAgent should return with STARTING before subprocess startup completes")
}

// TestOpenAgent_DelayedStartupBroadcastsActive asserts the goroutine
// emits ACTIVE once startAgent eventually returns.
func TestOpenAgent_DelayedStartupBroadcastsActive(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	releaseAfter := 150 * time.Millisecond
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		time.Sleep(releaseAfter)
		return &leapmuxv1.AgentSettings{}, nil
	}

	// Subscribe before opening so the broadcast is captured.
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	// Watch the agent to capture broadcasts.
	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			ae := resp.GetAgentEvent()
			if ae == nil {
				continue
			}
			if sc := ae.GetStatusChange(); sc != nil {
				if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
					return true
				}
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast after delayed startup")
}

// TestOpenAgent_ResponseHasNilGitStatus asserts that the immediate
// OpenAgent response carries no gitStatus — it's deliberately moved
// off the sync path and emitted via a subsequent STARTING broadcast so
// the RPC returns without forking `git status`.
func TestOpenAgent_ResponseHasNilGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	blocked := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return &leapmuxv1.AgentSettings{}, nil
	}
	defer close(blocked)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Nil(t, resp.GetAgent().GetGitStatus(),
		"OpenAgent response must not carry gitStatus — it's deferred to the async startup broadcast")
}

// TestOpenTerminal_CatchUpReplaySurfacesStartupMessage regression-tests
// the bug where a newly-opened terminal tab showed a generic "Starting
// terminal…" label instead of the backend-provided "Starting <shell>…".
//
// The client subscribes to WatchEvents only AFTER receiving the
// OpenTerminal response, so the sync-path STARTING broadcast lands with
// no watchers registered. The fix stores the phase label in the
// TerminalStartup registry so deriveTerminalStatus → the WatchEvents
// catch-up replay can surface it to the just-arriving subscriber.
func TestOpenTerminal_CatchUpReplaySurfacesStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	// Block the PTY spawn indefinitely so the terminal stays in STARTING
	// long enough for the WatchEvents catch-up replay to read the
	// registry.
	blocked := make(chan struct{})
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "/bin/zsh",
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		TerminalIds: []string{terminalID},
	}, wWatch)

	// Catch-up replay fires synchronously during the WatchEvents handler,
	// so the streams slice is already populated when dispatch returns.
	var sawStartingWithMsg bool
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetTerminalEvent().GetStatusChange()
		if sc == nil {
			continue
		}
		if sc.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING {
			assert.Equal(t, "Starting zsh…", sc.GetStartupMessage(),
				"catch-up replay must surface the phase label the OpenTerminal sync prologue registered")
			sawStartingWithMsg = true
		}
	}
	assert.True(t, sawStartingWithMsg,
		"expected a STARTING statusChange in the catch-up replay")
}

// TestOpenTerminal_ResolvesDefaultShellForStartupMessage covers the
// frontend path: handleOpenTerminal sends shell="" and expects the
// backend to pick the default. The startup-panel label must therefore
// name the *resolved* binary ("Starting zsh…"), not fall back to a
// generic "Starting terminal…". Regression test for the bug where the
// label was computed from r.GetShell() before resolution.
func TestOpenTerminal_ResolvesDefaultShellForStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	blocked := make(chan struct{})
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "", // frontend default: let the backend resolve.
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()

	_, _, msg, ok := svc.TerminalStartup.status(terminalID)
	require.True(t, ok, "terminal should still be in the STARTING registry (PTY spawn is blocked)")
	assert.True(t, strings.HasPrefix(msg, "Starting "),
		"startup message should start with 'Starting ' — got %q", msg)
	assert.NotEqual(t, "Starting terminal…", msg,
		"startup message must name the resolved shell, not the generic fallback (got %q)", msg)
}

// TestListTerminals_SurfacesRegistryStartupMessage verifies that the
// ListTerminals handler includes startup_message on the TerminalInfo
// for terminals that are currently STARTING in the registry. Without
// this, a client refreshing mid-startup (e.g. hard reload during PTY
// spawn) falls back to the generic "Starting terminal…" label.
func TestListTerminals_SurfacesRegistryStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	blocked := make(chan struct{})
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "/usr/bin/fish",
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()

	wList := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{terminalID},
	}, wList)
	require.Empty(t, wList.errors)
	require.Len(t, wList.responses, 1)

	var listResp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(wList.responses[0].GetPayload(), &listResp))
	require.Len(t, listResp.GetTerminals(), 1)
	ti := listResp.GetTerminals()[0]
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, ti.GetStatus())
	assert.Equal(t, "Starting fish…", ti.GetStartupMessage())
}

// TestOpenAgent_CatchUpReplaySurfacesStartupMessage mirrors the
// terminal regression test for agents: a WatchEvents subscriber that
// attaches after the initial STARTING broadcast should see the current
// phase label via catch-up replay, not an empty string.
func TestOpenAgent_CatchUpReplaySurfacesStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	// Block startAgent so the goroutine settles after setting phase 2
	// ("Starting Claude Code…") and waits there — the registry entry
	// then holds that phase label for replay.
	blocked := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return &leapmuxv1.AgentSettings{}, nil
	}
	defer close(blocked)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Wait until the goroutine has progressed past phase 2 so the
	// registry holds "Starting Claude Code…" (the last registered
	// message before startAgent blocks).
	require.Eventually(t, func() bool {
		_, _, msg, ok := svc.AgentStartup.status(agentID)
		return ok && msg == "Starting Claude Code…"
	}, 5*time.Second, 20*time.Millisecond,
		"expected runAgentStartup to reach phase 2 before startAgent blocks")

	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	}, wWatch)

	var sawStartingWithMsg bool
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetAgentEvent().GetStatusChange()
		if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_STARTING {
			continue
		}
		assert.Equal(t, "Starting Claude Code…", sc.GetStartupMessage(),
			"catch-up replay must surface the phase label stored in the registry")
		sawStartingWithMsg = true
	}
	assert.True(t, sawStartingWithMsg,
		"expected a STARTING statusChange in the catch-up replay")
}

// TestOpenAgent_ActiveBroadcastCarriesGitStatus asserts that the final
// ACTIVE broadcast emitted by runAgentStartup carries the gitStatus
// computed in the pre-startAgent phase. Phase-ordering of the
// intermediate STARTING broadcasts is not asserted here (they may land
// before the test's WatchEvents subscription registers, since the
// runAgentStartup goroutine fires concurrently with the RPC response);
// TestBuildAgentStatusChange verifies the phase/error/gitStatus field
// mapping directly, race-free.
func TestOpenAgent_ActiveBroadcastCarriesGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	// Block startAgent briefly so the test can subscribe before ACTIVE lands.
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		time.Sleep(100 * time.Millisecond)
		return &leapmuxv1.AgentSettings{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				continue
			}
			assert.NotNil(t, sc.GetGitStatus(), "ACTIVE broadcast must carry gitStatus")
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast with gitStatus")
}

// TestBuildAgentStatusChange covers how the agentStatusDetails fields
// flow into the AgentStatusChange proto. This is the race-free
// companion to TestOpenAgent_ActiveBroadcastCarriesGitStatus: it locks
// in the (gitStatus, startupError, startupMessage) mapping without
// routing through the broadcast fan-out.
func TestBuildAgentStatusChange(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")
	dbAgent := &db.Agent{
		ID:            "agent-bac",
		WorkspaceID:   "ws-1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Model:         "opus[1m]",
		Effort:        "xhigh",
		WorkingDir:    t.TempDir(),
	}

	t.Run("STARTING carries startupMessage and gitStatus, no AvailableModels", func(t *testing.T) {
		gs := &leapmuxv1.AgentGitStatus{Branch: "main", OriginUrl: "https://example.com/repo.git"}
		sc := svc.buildAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, agentStatusDetails{
			gitStatus:      gs,
			startupMessage: "Checking Git status…",
		})
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, sc.GetStatus())
		assert.Equal(t, "Checking Git status…", sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
		assert.Same(t, gs, sc.GetGitStatus(), "gitStatus must flow through without a copy")
		// AvailableModels / OptionGroups are deliberately skipped for non-ACTIVE
		// so a STARTING broadcast doesn't overwrite the frontend's last-known
		// catalog with an empty slice.
		assert.Empty(t, sc.GetAvailableModels())
		assert.Empty(t, sc.GetAvailableOptionGroups())
	})

	t.Run("STARTUP_FAILED carries startupError and gitStatus", func(t *testing.T) {
		gs := &leapmuxv1.AgentGitStatus{Branch: "main"}
		sc := svc.buildAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, agentStatusDetails{
			gitStatus:    gs,
			startupError: "exec: claude: not found",
		})
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, sc.GetStatus())
		assert.Equal(t, "exec: claude: not found", sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
		assert.Same(t, gs, sc.GetGitStatus())
	})

	t.Run("empty details produce nil gitStatus and blank message/error", func(t *testing.T) {
		sc := svc.buildAgentStatusChange(dbAgent, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, agentStatusDetails{})
		assert.Nil(t, sc.GetGitStatus(), "nil gitStatus must flow through unchanged (no auto-fetch)")
		assert.Empty(t, sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
	})
}

// TestBuildTerminalStatusChange covers the terminalStatusDetails
// mapping, mirroring TestBuildAgentStatusChange. Locks in the
// (startupError, startupMessage) mapping for the race-free path.
func TestBuildTerminalStatusChange(t *testing.T) {
	t.Run("STARTING carries startupMessage, empty error", func(t *testing.T) {
		sc := buildTerminalStatusChange("term-1", leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, terminalStatusDetails{
			startupMessage: "Starting zsh…",
		})
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, sc.GetStatus())
		assert.Equal(t, "Starting zsh…", sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
	})

	t.Run("STARTUP_FAILED carries startupError, empty message", func(t *testing.T) {
		sc := buildTerminalStatusChange("term-1", leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, terminalStatusDetails{
			startupError: "fork: permission denied",
		})
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, sc.GetStatus())
		assert.Equal(t, "fork: permission denied", sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
	})

	t.Run("READY produces blank message and error", func(t *testing.T) {
		sc := buildTerminalStatusChange("term-1", leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, terminalStatusDetails{})
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, sc.GetStatus())
		assert.Empty(t, sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
	})
}

// TestOpenAgent_StartupFailurePhaseCarriesGitStatus asserts that when
// startAgent fails, the STARTUP_FAILED broadcast still carries the
// gitStatus computed during the pre-startAgent phase, so the frontend
// can render branch info alongside the error.
func TestOpenAgent_StartupFailurePhaseCarriesGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("forced startup failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	wWatch := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil {
				continue
			}
			if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
				assert.NotEmpty(t, sc.GetStartupError(), "STARTUP_FAILED must carry the error")
				assert.NotNil(t, sc.GetGitStatus(),
					"STARTUP_FAILED must still carry gitStatus computed pre-startAgent")
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected STARTUP_FAILED broadcast")
}

// TestOpenAgent_StartupFailureBroadcastsFailureAndRollsBack asserts that
// a startAgentFn error produces a STARTUP_FAILED status with the error
// string visible to a subscribed watcher, and that the agent is closed.
func TestOpenAgent_StartupFailureBroadcastsFailureAndRollsBack(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	var startCalls sync.WaitGroup
	startCalls.Add(1)
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		defer startCalls.Done()
		return nil, errors.New("forced startup failure: boom")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	startCalls.Wait()

	// Agent's DB row stays open after startup failure — the in-tab
	// error UI remains reachable across page refreshes; the user
	// dismisses the failed agent via the "Close tab" button which
	// calls CloseAgent.
	require.Eventually(t, func() bool {
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		return err == nil && !row.ClosedAt.Valid
	}, 5*time.Second, 20*time.Millisecond, "agent DB row should stay open after startup failure")

	// Startup registry should report STARTUP_FAILED with the error.
	require.Eventually(t, func() bool {
		status, errStr, _, ok := svc.AgentStartup.status(agentID)
		return ok && status == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED && errStr != ""
	}, 5*time.Second, 20*time.Millisecond, "expected Startup registry to retain STARTUP_FAILED")

}
