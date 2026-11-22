package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func testStartupPrompt() agent.StartupPrompt {
	return agent.StartupPrompt{
		Title:          "macOS Keychain access required",
		Description:    "Need Keychain access to decrypt credentials.",
		Steps:          []string{"Click Proceed.", "Click Always Allow."},
		Note:           "Remote Workers show the macOS dialog on the Worker Mac.",
		ConfirmLabel:   "Proceed",
		CancelLabel:    "Cancel",
		WaitingMessage: "Waiting for macOS Keychain confirmation…",
		ProceedMessage: "Waiting for macOS Keychain authorization…",
	}
}

func waitForPendingPrompt(t *testing.T, svc *Service, agentID string) *leapmuxv1.AgentStartupPrompt {
	t.Helper()
	var prompt *leapmuxv1.AgentStartupPrompt
	require.Eventually(t, func() bool {
		snap, ok := svc.AgentStartup.readSnapshot(agentID)
		if !ok || snap.startupPrompt == nil {
			return false
		}
		prompt = snap.startupPrompt
		return true
	}, 10*time.Second, 10*time.Millisecond, "expected pending startup prompt for %s", agentID)
	return prompt
}

func listAgentByID(t *testing.T, d testRPCDispatcher, agentID string) *leapmuxv1.AgentInfo {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
		TabIds: []string{agentID},
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	return resp.GetAgents()[0]
}

func TestStartupPrompt_ProceedContinuesStartupAndClearsPrompt(t *testing.T) {
	t.Parallel()

	svc, d, openWriter := setupTestService(t)
	defer drainAllInFlight(svc)

	prompt := testStartupPrompt()
	proceedResult := make(chan struct {
		proceed bool
		err     error
	}, 1)
	enterPrompt := make(chan struct{})
	enterPromptOnce := sync.OnceFunc(func() { close(enterPrompt) })
	releaseStart := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(releaseStart) })
	defer func() {
		enterPromptOnce()
		releaseOnce()
		svc.AgentStartup.cancelPendingPromptsForShutdown()
	}()
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		require.NotNil(t, opts.ConfirmStartupPrompt)
		<-enterPrompt
		ok, err := opts.ConfirmStartupPrompt(ctx, prompt)
		proceedResult <- struct {
			proceed bool
			err     error
		}{proceed: ok, err: err}
		if err != nil {
			return nil, err
		}
		<-releaseStart
		return map[string]string{}, nil
	}

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, openWriter)
	require.Empty(t, openWriter.errors)
	require.Len(t, openWriter.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(openWriter.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Attach a live-only watcher before ConfirmStartupPrompt runs so we observe
	// the live opening STARTING broadcast carrying startup_prompt and WaitingMessage.
	liveWatcher := newTestWriter()
	registerAgentWatch(svc, "live-proceed-watcher", agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, liveWatcher)
	enterPromptOnce()

	pending := waitForPendingPrompt(t, svc, agentID)
	assert.Equal(t, prompt.Title, pending.GetTitle())
	assert.Equal(t, prompt.Steps, pending.GetSteps())

	require.Eventually(t, func() bool {
		for _, sc := range agentStatusChanges(t, liveWatcher, agentID) {
			if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING &&
				sc.GetStartupMessage() == prompt.WaitingMessage &&
				sc.GetStartupPrompt().GetPromptId() == pending.GetPromptId() {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "live watcher must receive opening STARTING broadcast with startup_prompt")

	// ListAgents includes the pending prompt and waiting message.
	listed := listAgentByID(t, d, agentID)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, listed.GetStatus())
	assert.Equal(t, prompt.WaitingMessage, listed.GetStartupMessage())
	require.NotNil(t, listed.GetStartupPrompt())
	assert.Equal(t, pending.GetPromptId(), listed.GetStartupPrompt().GetPromptId())

	// WatchEvents catch-up replay also includes the pending prompt.
	watcher := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId:  agentID,
			Replay:   leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			Mode:     leapmuxv1.WatchMode_WATCH_MODE_FULL,
			ReplayId: 1,
		}},
	}, watcher)
	waitForFullStartupAgentWatch(t, watcher, agentID)

	var replayChanges []*leapmuxv1.AgentStatusChange
	require.Eventually(t, func() bool {
		replayChanges = agentStatusChanges(t, watcher, agentID)
		return len(replayChanges) > 0
	}, 10*time.Second, 10*time.Millisecond)
	lastReplay := replayChanges[len(replayChanges)-1]
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, lastReplay.GetStatus())
	assert.Equal(t, prompt.WaitingMessage, lastReplay.GetStartupMessage())
	require.NotNil(t, lastReplay.GetStartupPrompt())
	assert.Equal(t, pending.GetPromptId(), lastReplay.GetStartupPrompt().GetPromptId())

	// Respond with Proceed.
	respWriter := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  agentID,
		PromptId: pending.GetPromptId(),
		Proceed:  true,
	}, respWriter)
	require.Empty(t, respWriter.errors)
	require.Len(t, respWriter.responses, 1)

	res := <-proceedResult
	require.NoError(t, res.err)
	assert.True(t, res.proceed)

	// Confirm the startup goroutine emitted the cleared STARTING(ProceedMessage)
	// before releasing startup to transition to ACTIVE.
	require.Eventually(t, func() bool {
		changes := agentStatusChanges(t, watcher, agentID)
		last := changes[len(changes)-1]
		return last.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING &&
			last.GetStartupPrompt() == nil &&
			last.GetStartupMessage() == prompt.ProceedMessage
	}, 10*time.Second, 10*time.Millisecond)

	releaseOnce()
	svc.AgentStartup.WaitForInFlight()

	// Final broadcast must be ACTIVE with no trailing STARTING after ACTIVE.
	require.Eventually(t, func() bool {
		changes := agentStatusChanges(t, watcher, agentID)
		return len(changes) > 0 && changes[len(changes)-1].GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE
	}, 10*time.Second, 10*time.Millisecond)
	changes := agentStatusChanges(t, watcher, agentID)
	activeIdx := -1
	for i, sc := range changes {
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
			activeIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, activeIdx, 0)
	for i := activeIdx + 1; i < len(changes); i++ {
		assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, changes[i].GetStatus(),
			"no out-of-order STARTING broadcast may follow ACTIVE")
	}
}

func TestStartupPrompt_CancelFailsStartup(t *testing.T) {
	t.Parallel()

	svc, d, openWriter := setupTestService(t)
	defer drainAllInFlight(svc)

	prompt := testStartupPrompt()
	releaseCancel := make(chan struct{})
	releaseCancelOnce := sync.OnceFunc(func() { close(releaseCancel) })
	defer func() {
		releaseCancelOnce()
		svc.AgentStartup.cancelPendingPromptsForShutdown()
	}()
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		proceed, err := opts.ConfirmStartupPrompt(ctx, prompt)
		if err != nil {
			return nil, err
		}
		if !proceed {
			<-releaseCancel
			return nil, errors.Join(agent.ErrStartupCanceled, errors.New("macOS Keychain access was not authorized"))
		}
		return map[string]string{}, nil
	}

	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, openWriter)
	require.Empty(t, openWriter.errors)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(openWriter.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	pending := waitForPendingPrompt(t, svc, agentID)
	watcher := watchAgent(t, d, agentID)

	respWriter := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  agentID,
		PromptId: pending.GetPromptId(),
		Proceed:  false,
	}, respWriter)
	require.Empty(t, respWriter.errors)

	// ConfirmStartupPrompt must synchronously broadcast a prompt-clearing STARTING
	// event before returning (false, nil), clearing the modal prior to rollback/failure.
	require.Eventually(t, func() bool {
		changes := agentStatusChanges(t, watcher, agentID)
		if len(changes) == 0 {
			return false
		}
		last := changes[len(changes)-1]
		return last.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING &&
			last.GetStartupPrompt() == nil &&
			last.GetStartupMessage() == prompt.WaitingMessage
	}, 10*time.Second, 10*time.Millisecond, "cancel must emit a live STARTING broadcast with nil startup_prompt before STARTUP_FAILED")

	releaseCancelOnce()
	svc.AgentStartup.WaitForInFlight()

	row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
	require.NoError(t, err)
	assert.Contains(t, row.StartupError, "macOS Keychain access was not authorized")

	listed := listAgentByID(t, d, agentID)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, listed.GetStatus())
	assert.Nil(t, listed.GetStartupPrompt())
	assert.Contains(t, listed.GetStartupError(), "macOS Keychain access was not authorized")

	require.Eventually(t, func() bool {
		changes := agentStatusChanges(t, watcher, agentID)
		return len(changes) > 0 && changes[len(changes)-1].GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED
	}, 10*time.Second, 10*time.Millisecond)
	changes := agentStatusChanges(t, watcher, agentID)
	failedIdx := -1
	for i, sc := range changes {
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
			failedIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, failedIdx, 0)
	assert.Nil(t, changes[failedIdx].GetStartupPrompt())
	for i := failedIdx + 1; i < len(changes); i++ {
		assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, changes[i].GetStatus(),
			"no late STARTING broadcast may follow STARTUP_FAILED")
	}
}

func TestStartupPrompt_RejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	svc, d, childID, rootID := setupChildAgentTest(t)
	defer drainAllInFlight(svc)

	// (a) Empty prompt_id -> InvalidArgument.
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  rootID,
		PromptId: "   ",
		Proceed:  true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)

	// (c) No pending prompt -> FailedPrecondition.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  rootID,
		PromptId: "prompt-none",
		Proceed:  true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)

	// (e) Subagent agent_id -> FailedPrecondition.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  childID,
		PromptId: "prompt-child",
		Proceed:  true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
	assert.Contains(t, w.errors[0].message, "only a root agent supports startup confirmations")

	// Start a real prompt on rootID to test (b) wrong/stale prompt_id and (d) duplicate response.
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		proceed, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
		if err != nil {
			return nil, err
		}
		if !proceed {
			return nil, agent.ErrStartupCanceled
		}
		return map[string]string{}, nil
	}
	openW := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, openW)
	require.Empty(t, openW.errors)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(openW.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	pending := waitForPendingPrompt(t, svc, agentID)

	// (b) Wrong prompt_id -> FailedPrecondition, pending prompt stays intact.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  agentID,
		PromptId: "wrong-id",
		Proceed:  true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
	stillPending, tracked := svc.AgentStartup.readSnapshot(agentID)
	require.True(t, tracked)
	require.NotNil(t, stillPending.startupPrompt)
	assert.Equal(t, pending.GetPromptId(), stillPending.startupPrompt.GetPromptId())

	// First valid response succeeds.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  agentID,
		PromptId: pending.GetPromptId(),
		Proceed:  false,
	}, w)
	require.Empty(t, w.errors)

	// (d) Duplicate second response -> FailedPrecondition.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
		AgentId:  agentID,
		PromptId: pending.GetPromptId(),
		Proceed:  true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)

	svc.AgentStartup.WaitForInFlight()
}

func TestStartupPrompt_CloseTabAndShutdownUnblockWaitingPrompt(t *testing.T) {
	t.Parallel()

	t.Run("CloseTab unblocks waiting prompt with errAgentClosedDuringLaunch", func(t *testing.T) {
		t.Parallel()

		svc, d, openW := setupTestService(t)
		defer drainAllInFlight(svc)

		confirmErrCh := make(chan error, 1)
		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			confirmErrCh <- err
			return nil, err
		}

		dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}, openW)
		require.Empty(t, openW.errors)
		var openResp leapmuxv1.OpenAgentResponse
		require.NoError(t, proto.Unmarshal(openW.responses[0].GetPayload(), &openResp))
		agentID := openResp.GetAgent().GetId()
		_ = waitForPendingPrompt(t, svc, agentID)

		closeW := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
			Tab: agentTabIdent(agentID),
		}, closeW)
		require.Empty(t, closeW.errors)

		assert.ErrorIs(t, <-confirmErrCh, errAgentClosedDuringLaunch)
		svc.AgentStartup.WaitForInFlight()

		row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
		require.NoError(t, err)
		assert.True(t, row.ClosedAt.Valid)
		assert.Empty(t, row.StartupError)
	})

	t.Run("Shutdown unblocks OpenAgent prompt, queued cold-start prompt, and awaitInFlight waiter", func(t *testing.T) {
		t.Parallel()

		svc, d, openW := setupTestService(t)
		var openStarts atomic.Int32
		openConfirmErr := make(chan error, 1)
		coldConfirmErr := make(chan error, 1)

		coldAgentID := "cold-prompt-agent"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            coldAgentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			if opts.AgentID == coldAgentID {
				_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				coldConfirmErr <- err
				return nil, err
			}
			openStarts.Add(1)
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			openConfirmErr <- err
			return nil, err
		}

		// 1. OpenAgent parked in ConfirmStartupPrompt.
		dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}, openW)
		require.Empty(t, openW.errors)
		var openResp leapmuxv1.OpenAgentResponse
		require.NoError(t, proto.Unmarshal(openW.responses[0].GetPayload(), &openResp))
		openAgentID := openResp.GetAgent().GetId()
		_ = waitForPendingPrompt(t, svc, openAgentID)

		// 2. Another ensureAgentRunning caller waiting in awaitInFlight behind openAgentID.
		waiterErrCh := make(chan error, 1)
		go func() {
			waiterErrCh <- svc.ensureAgentRunning(openAgentID, resumeIfConversation, interactiveStart)
		}()
		// 3. A queued cold-start parked in ConfirmStartupPrompt.
		coldErrCh := make(chan error, 1)
		go func() {
			coldErrCh <- svc.ensureAgentRunning(coldAgentID, resumeIfConversation, queuedStart)
		}()
		_ = waitForPendingPrompt(t, svc, coldAgentID)

		// Give the waiter goroutine a moment to enter awaitInFlight, then shut down.
		// Even if it enters slightly before or after Shutdown sets shuttingDown,
		// it must return errWorkerShuttingDown and never start a second process.
		watcher := watchAgent(t, d, openAgentID)
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			svc.Shutdown()
		}()

		select {
		case <-shutdownDone:
		case <-time.After(10 * time.Second):
			t.Fatal("Shutdown stalled behind pending startup prompts")
		}

		assert.ErrorIs(t, <-openConfirmErr, errWorkerShuttingDown)
		assert.ErrorIs(t, <-coldConfirmErr, errWorkerShuttingDown)
		assert.ErrorIs(t, <-waiterErrCh, errWorkerShuttingDown)
		assert.ErrorIs(t, <-coldErrCh, errWorkerShuttingDown)
		assert.Equal(t, int32(1), openStarts.Load(), "woken awaitInFlight waiter must not trigger a second startAgent")

		svc.AgentStartup.WaitForInFlight()
		for _, id := range []string{openAgentID, coldAgentID} {
			row, err := svc.Queries.GetAgentByID(t.Context(), id)
			require.NoError(t, err)
			assert.Empty(t, row.StartupError, "shutdown must not persist STARTUP_FAILED for %s", id)
		}
		for _, sc := range agentStatusChanges(t, watcher, openAgentID) {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, sc.GetStatus(),
				"woken awaitInFlight waiter must not broadcast a spurious STARTING during shutdown")
		}
	})
}

func TestStartupPrompt_BackgroundStartIsNonInteractiveAndColdStartIsInteractive(t *testing.T) {
	t.Parallel()

	t.Run("backgroundStart leaves ConfirmStartupPrompt nil", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "bg-agent"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		called := false
		svc.startBackgroundAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			called = true
			assert.Nil(t, opts.ConfirmStartupPrompt, "backgroundStart must remain non-interactive")
			return map[string]string{}, nil
		}

		err := svc.ensureAgentRunning(agentID, resumeStoredSession, backgroundStart)
		require.NoError(t, err)
		assert.True(t, called)
	})

	for _, tc := range []struct {
		name     string
		priority startPriority
	}{
		{name: "interactiveStart", priority: interactiveStart},
		{name: "queuedStart", priority: queuedStart},
	} {
		t.Run(tc.name+" wires ConfirmStartupPrompt resolvable via RPC", func(t *testing.T) {
			t.Parallel()

			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "cold-" + tc.name
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				require.NotNil(t, opts.ConfirmStartupPrompt)
				proceed, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				if err != nil {
					return nil, err
				}
				if !proceed {
					return nil, agent.ErrStartupCanceled
				}
				return map[string]string{}, nil
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- svc.ensureAgentRunning(agentID, resumeIfConversation, tc.priority)
			}()

			pending := waitForPendingPrompt(t, svc, agentID)
			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
				AgentId:  agentID,
				PromptId: pending.GetPromptId(),
				Proceed:  true,
			}, w)
			require.Empty(t, w.errors)

			require.NoError(t, <-errCh)
		})
	}
}

type syncBroadcastInspectWriter struct {
	*testResponseWriter
	onStream func(*leapmuxv1.InnerStreamMessage)
}

func (w *syncBroadcastInspectWriter) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	if w.onStream != nil {
		w.onStream(m)
	}
	return w.testResponseWriter.SendStream(m)
}

func TestStartupPrompt_ClearAndRestartPathsWirePromptAndPreserveSession(t *testing.T) {
	t.Parallel()

	t.Run("prepareClearContext ordinary clear failure removes hold before broadcastAgentFailed and leaves untracked", func(t *testing.T) {
		t.Parallel()

		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "clear-ordinary"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			require.NotNil(t, opts.ConfirmStartupPrompt)
			proceed, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			if err != nil {
				return nil, err
			}
			if !proceed {
				return nil, errors.Join(agent.ErrStartupCanceled, errors.New("canceled by user"))
			}
			return map[string]string{}, nil
		}

		// Inspect AgentStartup and deriveAgentStatus synchronously inside
		// broadcastAgentFailed so a regression that drops hold.handle only in
		// defer hold.release() fails immediately.
		var (
			observedFailedBroadcast atomic.Bool
			trackedAtBroadcast      atomic.Bool
			derivedStatusAtBcast    atomic.Int32
			promptNonNilAtBcast     atomic.Bool
		)
		inspectW := &syncBroadcastInspectWriter{
			testResponseWriter: newTestWriter(),
			onStream: func(m *leapmuxv1.InnerStreamMessage) {
				if len(m.GetPayload()) == 0 {
					return
				}
				var resp leapmuxv1.WatchEventsResponse
				if err := proto.Unmarshal(m.GetPayload(), &resp); err != nil {
					return
				}
				ev := resp.GetAgentEvent()
				if ev == nil || ev.GetAgentId() != agentID {
					return
				}
				sc := ev.GetStatusChange()
				if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
					return
				}
				_, _, _, tracked := svc.AgentStartup.status(agentID)
				trackedAtBroadcast.Store(tracked)
				listedAtBroadcast := listAgentByID(t, d, agentID)
				derivedStatusAtBcast.Store(int32(listedAtBroadcast.GetStatus()))
				promptNonNilAtBcast.Store(listedAtBroadcast.GetStartupPrompt() != nil)
				observedFailedBroadcast.Store(true)
			},
		}
		registerAgentWatch(svc, "inspect-ordinary-clear", agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, inspectW)

		errCh := make(chan error, 1)
		go func() {
			_, err := svc.prepareClearContext(agentID)
			errCh <- err
		}()

		pending := waitForPendingPrompt(t, svc, agentID)
		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
			AgentId:  agentID,
			PromptId: pending.GetPromptId(),
			Proceed:  false,
		}, w)
		require.Empty(t, w.errors)

		err := <-errCh
		require.ErrorIs(t, err, agent.ErrStartupCanceled)

		require.True(t, observedFailedBroadcast.Load(), "broadcastAgentFailed must deliver STARTUP_FAILED synchronously to registered watchers")
		assert.False(t, trackedAtBroadcast.Load(), "hold.handle must be removed from AgentStartup before broadcastAgentFailed")
		assert.Equal(t, int32(leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED), derivedStatusAtBcast.Load(),
			"deriveAgentStatus must report STARTUP_FAILED before broadcastAgentFailed")
		assert.False(t, promptNonNilAtBcast.Load(), "startupPrompt must be nil before broadcastAgentFailed")

		// Ordinary /clear leaves AgentStartup untracked (for relaunch_status_test.go)
		// while ListAgents derives STARTUP_FAILED from the persisted startup_error column.
		_, _, _, tracked := svc.AgentStartup.status(agentID)
		assert.False(t, tracked)
		listed := listAgentByID(t, d, agentID)
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, listed.GetStatus())
		assert.Nil(t, listed.GetStartupPrompt())
	})

	t.Run("prepareClearContext ordinary clear suppresses failure on close", func(t *testing.T) {
		t.Parallel()

		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "clear-ordinary-close"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			require.NotNil(t, opts.ConfirmStartupPrompt)
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			return nil, err
		}

		watcher := watchAgent(t, d, agentID)
		errCh := make(chan error, 1)
		go func() {
			_, err := svc.prepareClearContext(agentID)
			errCh <- err
		}()

		_ = waitForPendingPrompt(t, svc, agentID)
		closeW := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
			Tab: agentTabIdent(agentID),
		}, closeW)
		require.Empty(t, closeW.errors)

		require.ErrorIs(t, <-errCh, errAgentClosedDuringLaunch)
		row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
		require.NoError(t, err)
		assert.Empty(t, row.StartupError, "close during ordinary /clear must not persist STARTUP_FAILED")
		for _, sc := range agentStatusChanges(t, watcher, agentID) {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, sc.GetStatus())
		}
	})

	t.Run("prepareClearContext ordinary clear suppresses failure on archive", func(t *testing.T) {
		t.Parallel()

		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "clear-ordinary-archive"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			require.NotNil(t, opts.ConfirmStartupPrompt)
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			return nil, err
		}

		watcher := watchAgent(t, d, agentID)
		errCh := make(chan error, 1)
		go func() {
			_, err := svc.prepareClearContext(agentID)
			errCh <- err
		}()

		_ = waitForPendingPrompt(t, svc, agentID)
		applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
			&leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

		require.ErrorIs(t, <-errCh, errAgentArchivedDuringLaunch)
		row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
		require.NoError(t, err)
		assert.Empty(t, row.StartupError, "archive during ordinary /clear must not persist STARTUP_FAILED")
		for _, sc := range agentStatusChanges(t, watcher, agentID) {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, sc.GetStatus())
		}
	})

	t.Run("prepareClearContext recovery clear wires prompt and suppresses failure on archive", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "clear-recovery-archive"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))
		svc.persistAgentStartupError(agentID, "prior failure")

		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			require.NotNil(t, opts.ConfirmStartupPrompt)
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			return nil, err
		}

		errCh := make(chan error, 1)
		go func() {
			_, err := svc.prepareClearContext(agentID)
			errCh <- err
		}()

		_ = waitForPendingPrompt(t, svc, agentID)
		applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
			&leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

		assert.ErrorIs(t, <-errCh, errAgentArchivedDuringLaunch)
		row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
		require.NoError(t, err)
		assert.Equal(t, "prior failure", row.StartupError, "archival must preserve prior startup_error without overwriting it")
	})

	t.Run("prepareClearContext ordinary clear suppresses failure on shutdown even when hold.handle is nil", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		agentID := "clear-ordinary-shutdown"
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            agentID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}))

		svc.shuttingDown.Store(true)
		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			require.NotNil(t, opts.ConfirmStartupPrompt, "shutdown confirmer must be wired when hold.handle is nil during shutdown")
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			return nil, err
		}

		_, err := svc.prepareClearContext(agentID)
		require.ErrorIs(t, err, errWorkerShuttingDown)
		row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
		require.NoError(t, err)
		assert.Empty(t, row.StartupError, "shutdown during ordinary /clear must not persist STARTUP_FAILED")
	})

	t.Run("relaunchForStartupSettingsChange rebinds confirmer and shutdown suppresses INACTIVE/agent_error", func(t *testing.T) {
		t.Parallel()

		svc, d, openW := setupTestService(t)
		defer drainAllInFlight(svc)

		var startCount atomic.Int32
		relaunchStarted := make(chan struct{})
		svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
			n := startCount.Add(1)
			if n == 1 {
				// Simulate a mid-startup settings change by updating the DB row before returning.
				row, err := svc.Queries.GetAgentByID(ctx, opts.AgentID)
				require.NoError(t, err)
				_, err = casPersistAgentOptions(ctx, svc.Queries, opts.AgentID, row.Options, map[string]string{agent.OptionIDModel: "changed-model"})
				require.NoError(t, err)
				// Install an agent whose UpdateSettings returns AppliedLive: false so runAgentStartup relaunches.
				return map[string]string{}, nil
			}
			close(relaunchStarted)
			require.NotNil(t, opts.ConfirmStartupPrompt)
			_, err := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
			return nil, err
		}

		dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		}, openW)
		require.Empty(t, openW.errors)
		var openResp leapmuxv1.OpenAgentResponse
		require.NoError(t, proto.Unmarshal(openW.responses[0].GetPayload(), &openResp))
		agentID := openResp.GetAgent().GetId()

		select {
		case <-relaunchStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("relaunchForStartupSettingsChange did not start")
		}
		_ = waitForPendingPrompt(t, svc, agentID)
		watcher := watchAgent(t, d, agentID)

		svc.Shutdown()
		svc.AgentStartup.WaitForInFlight()

		for _, sc := range agentStatusChanges(t, watcher, agentID) {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, sc.GetStatus(),
				"shutdown during startup-settings relaunch must not broadcast INACTIVE")
		}
		msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
		require.NoError(t, err)
		for _, m := range msgs {
			assert.NotContains(t, string(m.Content), contracts.NotificationTypeAgentError)
		}
	})

	t.Run("relaunchForStartupSettingsChange overwrites retired confirmer when hold.handle is nil", func(t *testing.T) {
		t.Parallel()

		t.Run("shuttingDown replaces retired confirmer with shutdown confirmer", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)

			agentID := "relaunch-nil-shutdown"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			// Close claims and mark shutting down so holdRelaunch returns a nil handle.
			svc.shuttingDown.Store(true)
			svc.AgentStartup.closeClaims()

			var sentinelCalled atomic.Bool
			var gotErr error
			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				require.NotNil(t, opts.ConfirmStartupPrompt, "shutdown confirmer must replace retired confirmer")
				_, gotErr = opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, gotErr
			}

			opts := svc.baseAgentOptions(agentID, row.WorkingDir, row.AgentProvider)
			opts.ConfirmStartupPrompt = func(context.Context, agent.StartupPrompt) (bool, error) {
				sentinelCalled.Store(true)
				return true, nil
			}
			_, ok := svc.relaunchForStartupSettingsChange(agentID, row.AgentProvider, opts, row)
			assert.False(t, ok)
			assert.False(t, sentinelCalled.Load(), "retired confirmer must never be invoked")
			assert.ErrorIs(t, gotErr, errWorkerShuttingDown)
		})

		t.Run("existing claim while not shutting down clears retired confirmer to nil", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "relaunch-nil-existing-claim"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			// Hold an existing startup claim so holdRelaunch returns a nil handle while !shuttingDown.
			existingClaim, outcome := svc.AgentStartup.beginColdStart(agentID, func() {})
			require.Equal(t, claimAdmitted, outcome)
			defer func() {
				svc.AgentStartup.succeed(agentID, existingClaim)
				svc.AgentStartup.finishEntry(existingClaim)
			}()

			var gotConfirmer agent.StartupPromptConfirmer
			svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				gotConfirmer = opts.ConfirmStartupPrompt
				return nil, errors.New("stop relaunch")
			}

			opts := svc.baseAgentOptions(agentID, row.WorkingDir, row.AgentProvider)
			opts.ConfirmStartupPrompt = func(context.Context, agent.StartupPrompt) (bool, error) {
				t.Fatal("retired confirmer must be overwritten with nil")
				return false, nil
			}
			_, ok := svc.relaunchForStartupSettingsChange(agentID, row.AgentProvider, opts, row)
			assert.False(t, ok)
			assert.Nil(t, gotConfirmer, "retired confirmer must be cleared to nil when hold.handle is nil and not shutting down")
		})
	})

	t.Run("restartAgentPreservingSession preserves session on cancel and handles close, archive, and shutdown", func(t *testing.T) {
		t.Parallel()

		// 1. Prompt canceled -> preserves agent_session_id.
		t.Run("cancel preserves session_id", func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "restart-cancel"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				Resumed:       1,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-keep",
				ID:             agentID,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				require.NotNil(t, opts.ConfirmStartupPrompt)
				proceed, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				if promptErr != nil {
					return nil, promptErr
				}
				if !proceed {
					return nil, errors.Join(agent.ErrStartupCanceled, errors.New("canceled"))
				}
				return map[string]string{}, nil
			}

			errCh := make(chan error, 1)
			go func() {
				_, restartErr := svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndPending, nil)
				errCh <- restartErr
			}()

			pending := waitForPendingPrompt(t, svc, agentID)
			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
				AgentId:  agentID,
				PromptId: pending.GetPromptId(),
				Proceed:  false,
			}, w)
			require.Empty(t, w.errors)

			require.ErrorIs(t, <-errCh, agent.ErrStartupCanceled)
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-keep", after.AgentSessionID, "canceled prompt must preserve stored agent_session_id")
		})

		// 2. Archive interruption with restartTurnEndObserved -> pauses with AGENT_STOPPED (not DELIVERY_FAILED) and no agent_error.
		t.Run("archive pauses with AGENT_STOPPED and suppresses agent_error", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "restart-archive"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				Resumed:       1,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-archive",
				ID:             agentID,
			}))
			_, err := svc.InputQueue.TurnStarted(t.Context(), agentID, false)
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				_, restartErr := svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndObserved, nil)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
				&leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

			require.ErrorIs(t, <-errCh, errAgentArchivedDuringLaunch)
			snap, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.True(t, snap.Paused)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snap.PauseReason)

			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-archive", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			assert.Empty(t, msgs, "archive interruption must not persist an agent_error notification")
		})

		// 3. Shutdown interruption with restartTurnEndObserved -> does not pause with DELIVERY_FAILED and no agent_error.
		t.Run("shutdown suppresses DELIVERY_FAILED and agent_error", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)

			agentID := "restart-shutdown"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				Resumed:       1,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-shutdown",
				ID:             agentID,
			}))
			_, err := svc.InputQueue.TurnStarted(t.Context(), agentID, false)
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				_, restartErr := svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndObserved, nil)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			svc.Shutdown()

			require.ErrorIs(t, <-errCh, errWorkerShuttingDown)
			var pauseReason sql.NullInt64
			err = svc.DB.QueryRowContext(t.Context(),
				"SELECT pause_reason FROM agent_input_queue_state WHERE agent_id = ?", agentID).Scan(&pauseReason)
			if !errors.Is(err, sql.ErrNoRows) {
				require.NoError(t, err)
				if pauseReason.Valid {
					assert.NotEqual(t, int64(leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED), pauseReason.Int64)
				}
			}

			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-shutdown", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			assert.Empty(t, msgs, "shutdown interruption must not persist an agent_error notification")
		})

		// 3b. Shutdown interruption while InputQueue is still active -> proves
		// the !errors.Is(err, errWorkerShuttingDown) guard prevents DELIVERY_FAILED.
		t.Run("shutdown while InputQueue active skips DELIVERY_FAILED pause", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "restart-shutdown-active-queue"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				Resumed:       1,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-shutdown-active",
				ID:             agentID,
			}))
			_, err := svc.InputQueue.TurnStarted(t.Context(), agentID, false)
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				_, restartErr := svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndObserved, nil)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			// Cancel pending prompts for shutdown while InputQueue is still running,
			// matching the window between shuttingDown.Store(true) and InputQueue.Stop().
			svc.AgentStartup.cancelPendingPromptsForShutdown()

			require.ErrorIs(t, <-errCh, errWorkerShuttingDown)
			snap, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.NotEqual(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED, snap.PauseReason,
				"shutdown interruption must not overwrite the queue pause reason with DELIVERY_FAILED")

			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-shutdown-active", after.AgentSessionID)
		})

		// 4. Close interruption with restartTurnEndObserved -> pauses with AGENT_STOPPED, preserves session_id, no agent_error.
		t.Run("close pauses with AGENT_STOPPED and suppresses agent_error", func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "restart-close"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
				Resumed:       1,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-close",
				ID:             agentID,
			}))
			_, err := svc.InputQueue.TurnStarted(t.Context(), agentID, false)
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				_, restartErr := svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndObserved, nil)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			closeW := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
				Tab: agentTabIdent(agentID),
			}, closeW)
			require.Empty(t, closeW.errors)

			require.ErrorIs(t, <-errCh, errAgentClosedDuringLaunch)
			snap, err := svc.InputQueue.Snapshot(t.Context(), agentID)
			require.NoError(t, err)
			assert.True(t, snap.Paused)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snap.PauseReason)

			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-close", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			assert.Empty(t, msgs, "close interruption must not persist an agent_error notification")
		})
	})

	t.Run("restartPlanContextLocked wires prompt, preserves session on cancel, and suppresses agent_error on close, archive, and shutdown", func(t *testing.T) {
		t.Parallel()

		t.Run("cancel preserves session_id", func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "plan-cancel"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-plan",
				ID:             agentID,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				require.NotNil(t, opts.ConfirmStartupPrompt)
				proceed, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				if promptErr != nil {
					return nil, promptErr
				}
				if !proceed {
					return nil, errors.Join(agent.ErrStartupCanceled, errors.New("canceled"))
				}
				return map[string]string{}, nil
			}

			errCh := make(chan error, 1)
			go func() {
				unlock := svc.Agents.LockAgent(agentID)
				defer unlock()
				_, restartErr := svc.restartPlanContextLocked(agentID, "acceptEdits", row)
				errCh <- restartErr
			}()

			pending := waitForPendingPrompt(t, svc, agentID)
			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, &leapmuxv1.RespondAgentStartupPromptRequest{
				AgentId:  agentID,
				PromptId: pending.GetPromptId(),
				Proceed:  false,
			}, w)
			require.Empty(t, w.errors)

			require.ErrorIs(t, <-errCh, agent.ErrStartupCanceled)
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-plan", after.AgentSessionID)
		})

		t.Run("shutdown suppresses agent_error and preserves session_id", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)

			agentID := "plan-shutdown"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-plan-shutdown",
				ID:             agentID,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			// Seed a running process to verify DiscardOutputAndStopAgent does not interfere.
			_, err = svc.Agents.StartAgentWith(t.Context(), agent.Options{
				AgentID:       agentID,
				WorkingDir:    row.WorkingDir,
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			}, newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), claudetest.StartEcho)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				unlock := svc.Agents.LockAgent(agentID)
				defer unlock()
				_, restartErr := svc.restartPlanContextLocked(agentID, "acceptEdits", row)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			svc.Shutdown()

			require.ErrorIs(t, <-errCh, errWorkerShuttingDown)
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-plan-shutdown", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			for _, m := range msgs {
				assert.NotContains(t, string(m.Content), contracts.NotificationTypeAgentError)
			}
		})

		t.Run("close suppresses agent_error and preserves session_id", func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "plan-close"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-plan-close",
				ID:             agentID,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				unlock := svc.Agents.LockAgent(agentID)
				defer unlock()
				_, restartErr := svc.restartPlanContextLocked(agentID, "acceptEdits", row)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			closeW := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
				Tab: agentTabIdent(agentID),
			}, closeW)
			require.Empty(t, closeW.errors)

			require.ErrorIs(t, <-errCh, errAgentClosedDuringLaunch)
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-plan-close", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			for _, m := range msgs {
				assert.NotContains(t, string(m.Content), contracts.NotificationTypeAgentError)
			}
		})

		t.Run("archive suppresses agent_error and preserves session_id", func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			agentID := "plan-archive"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID:            agentID,
				WorkingDir:    t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
			}))
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
				AgentSessionID: "sess-plan-archive",
				ID:             agentID,
			}))
			row, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)

			svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				_, promptErr := opts.ConfirmStartupPrompt(ctx, testStartupPrompt())
				return nil, promptErr
			}

			errCh := make(chan error, 1)
			go func() {
				unlock := svc.Agents.LockAgent(agentID)
				defer unlock()
				_, restartErr := svc.restartPlanContextLocked(agentID, "acceptEdits", row)
				errCh <- restartErr
			}()

			_ = waitForPendingPrompt(t, svc, agentID)
			applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
				&leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

			require.ErrorIs(t, <-errCh, errAgentArchivedDuringLaunch)
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			assert.Equal(t, "sess-plan-archive", after.AgentSessionID)
			msgs, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			for _, m := range msgs {
				assert.NotContains(t, string(m.Content), contracts.NotificationTypeAgentError)
			}
		})
	})
}
