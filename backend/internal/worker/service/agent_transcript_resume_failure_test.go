package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueuedInputDoesNotRelaunchARefusedNativeResume pins that a message sent
// while a native resume starts never launches that refused resume again.
//
// The browser sends the message while the tab reads STARTING. The queue
// dispatch passes the startup check, because the open path did not fail yet,
// and it joins the startup that is in flight. The provider then refuses
// session/load, and failStartup records STARTUP_FAILED. The join must report
// that failure. A join that read only "the startup settled" started the same
// refused resume a second time, with the session id that the dispatch read
// before the failure. A provider can damage its stored copy of the session on
// each launch, so the second launch can make the session impossible to resume.
func TestQueuedInputDoesNotRelaunchARefusedNativeResume(t *testing.T) {
	t.Parallel()

	// Quartz controls the join timer. The trap proves that the dispatch joined
	// the open path's startup before that startup failed.
	clock := testutil.NewQuartzMock(t)
	svc, dispatcher, _ := setupTestService(t, withClock(clock))
	t.Cleanup(func() { drainAllInFlight(svc) })
	ctx := testutil.DeadlineContext(t)
	joinTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
	defer joinTimer.Close()

	const sessionID = "native-gemini-session"
	refusal := errors.New(`session/load: could not resume session "native-gemini-session": ` +
		`json-rpc error -32603: Internal error (send /clear to start a fresh session)`)
	var launchMu sync.Mutex
	var launchedResumeIDs []string
	openLaunched := make(chan struct{})
	releaseOpenLaunch := make(chan struct{})
	svc.startAgentFn = func(startCtx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		launchMu.Lock()
		launchedResumeIDs = append(launchedResumeIDs, opts.ResumeSessionID)
		first := len(launchedResumeIDs) == 1
		launchMu.Unlock()
		if first {
			// The open path's launch holds the tab in STARTING until the test
			// releases the provider's refusal.
			close(openLaunched)
			select {
			case <-releaseOpenLaunch:
			case <-startCtx.Done():
				return nil, startCtx.Err()
			}
		}
		return nil, refusal
	}
	launched := func() []string {
		launchMu.Lock()
		defer launchMu.Unlock()
		return append([]string(nil), launchedResumeIDs...)
	}

	agentID := openTranscriptResumeAgent(t, dispatcher, t.TempDir(), sessionID, leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI)
	select {
	case <-openLaunched:
	case <-ctx.Done():
		require.FailNow(t, "the open path never launched the native resume")
	}

	inputID := newTestAgentInputID()
	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: inputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "Reply after the native picker reopens the session.",
	})
	require.NoError(t, err)
	joinTimer.MustWait(ctx).MustRelease(ctx)

	// The provider refuses session/load.
	close(releaseOpenLaunch)

	var failedItem inputqueue.SnapshotItem
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		if snapshotErr != nil {
			return false
		}
		for _, item := range snapshot.Items {
			if item.ID == inputID && item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED {
				failedItem = item
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond, "the queued input never reached a final state")

	assert.Equal(t, []string{sessionID}, launched(),
		"the Worker launched the refused resume again; the provider can destroy the session on that launch")
	assert.Equal(t, errAgentStartupFailed.Error(), failedItem.Error,
		"the queued input must carry the startup refusal, not the error of a second launch")
	status, startupError, _, tracked := svc.AgentStartup.status(agentID)
	assert.True(t, tracked, "the second launch replaced the failed startup record")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Equal(t, refusal.Error(), startupError)
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, refusal.Error(), row.StartupError)
}

func TestFailedNativeResumeRetainsTheCopiedWorkerTranscript(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI
	const sourceID = "closed-gemini-source"
	const sessionID = "native-gemini-session"
	const nativeError = "No previous sessions found for this project."
	seedTranscriptSource(t, svc, sourceID, workingDir, sessionID, provider, false)
	contents := []struct {
		source leapmuxv1.MessageSource
		bytes  string
		key    string
	}{
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, `{"content":"Keep the stored Worker prompt."}`, "native-user:1"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"sessionId":"native-gemini-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The stored Worker answer remains."}}}`, "native-answer:1"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`, "native-turn:1"},
	}
	for index, content := range contents {
		_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
			ID: sourceID + content.key, AgentID: sourceID, AgentSessionID: sessionID,
			Source: content.source, Content: []byte(content.bytes), IdempotencyKey: content.key,
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			AgentProvider:                  provider, CreatedAt: sqltime.NewSQLiteTime(time.Unix(100+int64(index), 0)),
		})
		require.NoError(t, err)
	}
	claimed, err := svc.Queries.ClaimAgentTurnEnd(t.Context(), db.ClaimAgentTurnEndParams{
		AgentID: sourceID, AgentSessionID: sessionID, IdempotencyKey: contents[2].key,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed)
	_, err = svc.Queries.CloseAgent(t.Context(), sourceID)
	require.NoError(t, err)
	original, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sourceID})
	require.NoError(t, err)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New(nativeError)
	}
	targetID := openTranscriptResumeAgent(t, dispatcher, workingDir, sessionID, provider)
	drainAllInFlight(svc)
	target, err := svc.Queries.GetAgentByID(t.Context(), targetID)
	require.NoError(t, err)
	assert.Contains(t, target.StartupError, nativeError)
	assert.Empty(t, target.AgentSessionID)
	assert.Empty(t, target.PendingResumeSessionID)
	status, startupError, _ := deriveAgentStatus(&target, svc.agentLivenessOf(&target))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Contains(t, startupError, nativeError)
	copied, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID})
	require.NoError(t, err)
	require.Len(t, copied, len(original))
	for index, source := range original {
		assert.NotEqual(t, source.ID, copied[index].ID)
		assert.Equal(t, targetID, copied[index].AgentID)
		assert.Equal(t, source.Seq, copied[index].Seq)
		assert.Equal(t, source.AgentSessionID, copied[index].AgentSessionID)
		assert.Equal(t, source.Source, copied[index].Source)
		assert.Equal(t, source.Content, copied[index].Content)
		assert.Equal(t, source.IdempotencyKey, copied[index].IdempotencyKey)
	}
	stored, err := svc.Queries.HasAgentTurnEnd(t.Context(), db.HasAgentTurnEndParams{
		AgentID: targetID, AgentSessionID: sessionID, IdempotencyKey: contents[2].key,
	})
	require.NoError(t, err)
	assert.True(t, stored)
	retained, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sourceID})
	require.NoError(t, err)
	assert.Equal(t, original, retained)
}

// refusedResumeSessionID is the native session whose resume the provider
// refuses in openRefusedNativeResume.
const refusedResumeSessionID = "native-gemini-session"

// resumeLaunches records the resume session ID of each launch of one agent. The
// first launch is the open path, which the provider refuses. Each later launch
// runs the launcher that the test gives through answerWith.
type resumeLaunches struct {
	mu        sync.Mutex
	resumeIDs []string
	after     agentLauncher
}

func (l *resumeLaunches) start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
	l.mu.Lock()
	l.resumeIDs = append(l.resumeIDs, opts.ResumeSessionID)
	first := len(l.resumeIDs) == 1
	after := l.after
	l.mu.Unlock()
	if first {
		return nil, errors.New(`session/load: could not resume session "` + refusedResumeSessionID + `": ` +
			`json-rpc error -32603: Internal error (send /clear to start a fresh session)`)
	}
	if after == nil {
		return nil, errors.New("the test gave no launcher for a launch after the refused resume")
	}
	return after(ctx, opts, sink)
}

func (l *resumeLaunches) answerWith(launch agentLauncher) {
	l.mu.Lock()
	l.after = launch
	l.mu.Unlock()
}

func (l *resumeLaunches) ids() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.resumeIDs...)
}

// openRefusedNativeResume opens an agent from a native session whose resume
// the provider refuses. The open copies the Worker transcript of a closed
// source agent first, and the copied rows are what it returns.
func openRefusedNativeResume(t *testing.T) (*Service, string, []db.Message, *resumeLaunches) {
	t.Helper()
	svc, dispatcher := transcriptResumeService(t)
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI
	workingDir := t.TempDir()
	const sourceID = "closed-gemini-source"
	seedTranscriptSource(t, svc, sourceID, workingDir, refusedResumeSessionID, provider, false)
	for index, content := range []struct {
		source leapmuxv1.MessageSource
		bytes  string
		key    string
	}{
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, `{"content":"Keep the stored Worker prompt."}`, "native-user:1"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"sessionId":"native-gemini-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The stored Worker answer remains."}}}`, "native-answer:1"},
	} {
		_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
			ID: sourceID + content.key, AgentID: sourceID, AgentSessionID: refusedResumeSessionID,
			Source: content.source, Content: []byte(content.bytes), IdempotencyKey: content.key,
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			AgentProvider:                  provider, CreatedAt: sqltime.NewSQLiteTime(time.Unix(100+int64(index), 0)),
		})
		require.NoError(t, err)
	}
	_, err := svc.Queries.CloseAgent(t.Context(), sourceID)
	require.NoError(t, err)

	launches := &resumeLaunches{}
	svc.startAgentFn = launches.start
	agentID := openTranscriptResumeAgent(t, dispatcher, workingDir, refusedResumeSessionID, provider)
	drainAllInFlight(svc)
	require.Equal(t, []string{refusedResumeSessionID}, launches.ids(), "fixture check: the open path launched the resume once")
	row := requireAgentRow(t, svc, agentID)
	require.True(t, svc.agentStartupFailed(&row), "fixture check: the refused resume is a recorded startup failure")
	copied, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	require.Len(t, copied, 2, "fixture check: the open copied the source transcript")
	return svc, agentID, copied, launches
}

// enqueueAndSettle queues one input and waits until the queue accepted it or
// failed it. It returns the error that a failed input carries, or "" for an
// accepted input.
//
// A failed input pauses the queue and stays at its head, so the next input
// would never dispatch. The helper removes a failed input and resumes the
// queue, as a user does before the next attempt.
func enqueueAndSettle(t *testing.T, svc *Service, agentID string, kind leapmuxv1.AgentInputKind, text string) string {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	inputID := newTestAgentInputID()
	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{ID: inputID, AgentID: agentID, Kind: kind, Text: text})
	require.NoError(t, err)
	var failure string
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		if snapshotErr != nil {
			return false
		}
		for _, item := range snapshot.Items {
			if item.ID != inputID {
				continue
			}
			if item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED {
				return false
			}
			failure = item.Error
			return true
		}
		// The queue removes an input that the provider accepted.
		return true
	}, inputQueueWait, 10*time.Millisecond, "the queued input never reached a final state")
	if failure != "" {
		_, err = svc.InputQueue.Delete(ctx, agentID, inputID)
		require.NoError(t, err)
		_, err = svc.InputQueue.SetPaused(ctx, agentID, false)
		require.NoError(t, err)
	}
	return failure
}

// requireMessageIDs fails the test unless every message in want is still in the
// transcript of agentID.
func requireMessageIDs(t *testing.T, svc *Service, agentID string, want []db.Message) {
	t.Helper()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	kept := make(map[string]bool, len(rows))
	for _, row := range rows {
		kept[row.ID] = true
	}
	for _, row := range want {
		assert.True(t, kept[row.ID], "the transcript lost the copied row %s", row.ID)
	}
}

// TestClearRecoversAnAgentWhoseNativeResumeWasRefused pins the recovery that
// the refusal tells the user to take: "send /clear to start a fresh session".
//
// A clear is an explicit action of the user, so it may start an agent whose
// startup failed, which no automatic path may do. It must start a NEW native
// session and never repeat the refused resume, because a provider can damage
// its stored copy of the session on each launch. It must remove the record of
// the failure, so the agent takes input again, and it must keep the
// transcript that the open copied.
func TestClearRecoversAnAgentWhoseNativeResumeWasRefused(t *testing.T) {
	t.Parallel()

	svc, agentID, copied, launches := openRefusedNativeResume(t)
	launches.answerWith(startWith(svc.Agents, claudetest.StartEcho))
	t.Cleanup(func() { svc.Agents.StopAgent(agentID) })

	failure := enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, "/clear")
	require.Empty(t, failure, "the queue refused the /clear that the refusal tells the user to send")

	assert.Equal(t, []string{refusedResumeSessionID, ""}, launches.ids(),
		"the clear must start a fresh session and never repeat the refused resume")
	row := requireAgentRow(t, svc, agentID)
	assert.Empty(t, row.StartupError, "the recovered agent still carries the startup failure in its row")
	_, _, _, tracked := svc.AgentStartup.status(agentID)
	assert.False(t, tracked, "the recovered agent still carries the startup failure in the registry")
	status, startupError, _ := deriveAgentStatus(&row, svc.agentLivenessOf(&row))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE.String(), status.String())
	assert.Empty(t, startupError)
	requireMessageIDs(t, svc, agentID, copied)

	// The recovered agent takes ordinary input again.
	assert.Empty(t, enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, "hello"),
		"the recovered agent refused a message")
	assert.Equal(t, []string{refusedResumeSessionID, ""}, launches.ids(),
		"a message to the running agent must not launch it again")
}

// TestAutomaticPathsStillRefuseAnAgentWhoseNativeResumeWasRefused pins the
// limit of that recovery. Only the clear recovers the agent. Ordinary input,
// a compaction, a control request and the resume sweep stay refused, and none
// of them launches the refused resume again.
func TestAutomaticPathsStillRefuseAnAgentWhoseNativeResumeWasRefused(t *testing.T) {
	t.Parallel()

	svc, agentID, copied, launches := openRefusedNativeResume(t)
	launches.answerWith(startWith(svc.Agents, claudetest.StartEcho))
	t.Cleanup(func() { svc.Agents.StopAgent(agentID) })

	assert.Equal(t, errAgentStartupFailed.Error(),
		enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, "hello"))
	assert.Equal(t, errAgentStartupFailed.Error(),
		enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, "/compact"))
	assert.False(t, svc.handleControlRequestMessage(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`),
		"a control request reached an agent whose startup failed")
	assert.Equal(t, outcomeSkipped, svc.AgentResumer().resumeOne(t.Context(), agentID))

	assert.Equal(t, []string{refusedResumeSessionID}, launches.ids(),
		"an automatic path launched the refused resume again")
	row := requireAgentRow(t, svc, agentID)
	assert.True(t, svc.agentStartupFailed(&row), "a refused path erased the record of the failure")
	requireMessageIDs(t, svc, agentID, copied)
}

// TestARecoveringClearIsAStartupThatACloseCancels pins the claim that the
// recovery takes. While the fresh session starts, the registry replays
// STARTING with the clear's label, as it does for every other start. A close
// in that window reaches the launch through the claim and cancels it, and the
// cancelled launch records no startup failure for a tab that is going away.
func TestARecoveringClearIsAStartupThatACloseCancels(t *testing.T) {
	t.Parallel()

	svc, agentID, _, launches := openRefusedNativeResume(t)
	ctx := testutil.DeadlineContext(t)
	launched := make(chan context.Context, 1)
	launches.answerWith(func(launchCtx context.Context, _ agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		launched <- launchCtx
		<-launchCtx.Done()
		return nil, launchCtx.Err()
	})
	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "/clear",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
	})
	require.NoError(t, err)
	var launchCtx context.Context
	select {
	case launchCtx = <-launched:
	case <-ctx.Done():
		require.FailNow(t, "the clear never launched a fresh session")
	}

	status, startupError, startupMessage, tracked := svc.AgentStartup.status(agentID)
	require.True(t, tracked, "the recovering clear holds no startup claim")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING.String(), status.String())
	assert.Empty(t, startupError)
	assert.Equal(t, agentStartupLabel("Restarting", leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI), startupMessage)

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", agentID)
	assert.ErrorIs(t, launchCtx.Err(), context.Canceled, "the close did not reach the launch of the recovering clear")
	drainAllInFlight(svc)

	_, _, _, tracked = svc.AgentStartup.status(agentID)
	assert.False(t, tracked, "the cancelled launch recorded a startup failure for a closed tab")
	row := requireAgentRow(t, svc, agentID)
	assert.True(t, row.ClosedAt.Valid)
	assert.NotContains(t, row.StartupError, context.Canceled.Error(),
		"the cancelled launch recorded a startup failure for a closed tab")
	assert.Equal(t, []string{refusedResumeSessionID, ""}, launches.ids())
}

// TestAFailedClearKeepsTheAgentFailed pins the failure path of the recovery. A
// clear whose fresh session also fails to start leaves an agent whose startup
// failed: the registry and the row state the NEW failure, and the automatic
// paths stay refused. A later clear can still recover the agent.
func TestAFailedClearKeepsTheAgentFailed(t *testing.T) {
	t.Parallel()

	svc, agentID, copied, launches := openRefusedNativeResume(t)
	const quota = "gemini: quota exceeded"
	launches.answerWith(func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New(quota)
	})

	failure := enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, "/clear")
	assert.Contains(t, failure, quota, "the clear must report the failure of its own launch")
	assert.Equal(t, []string{refusedResumeSessionID, ""}, launches.ids(),
		"the clear must launch a fresh session once")

	row := requireAgentRow(t, svc, agentID)
	assert.Contains(t, row.StartupError, quota)
	registryStatus, registryError, _, tracked := svc.AgentStartup.status(agentID)
	require.True(t, tracked, "the registry lost the record of the failure")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED.String(), registryStatus.String())
	assert.Contains(t, registryError, quota, "the registry states the earlier failure, and the row states the new one")
	assert.Equal(t, errAgentStartupFailed.Error(),
		enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, "hello"))
	assert.Equal(t, outcomeSkipped, svc.AgentResumer().resumeOne(t.Context(), agentID))
	assert.Len(t, launches.ids(), 2, "an automatic path launched the agent after the failed clear")

	// A later clear recovers the agent.
	launches.answerWith(startWith(svc.Agents, claudetest.StartEcho))
	t.Cleanup(func() { svc.Agents.StopAgent(agentID) })
	require.Empty(t, enqueueAndSettle(t, svc, agentID, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, "/clear"))
	assert.Equal(t, []string{refusedResumeSessionID, "", ""}, launches.ids())
	assert.Empty(t, requireAgentRow(t, svc, agentID).StartupError)
	requireMessageIDs(t, svc, agentID, copied)
}
