package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedChildRowRead struct {
	db.DBTX
	childID string
	read    func(context.Context, string, ...any) *sql.Row
}

func (reader observedChildRowRead) QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row {
	if strings.Contains(query, "-- name: GetAgentByID :one") && len(arguments) == 1 && arguments[0] == reader.childID {
		return reader.read(ctx, query, arguments...)
	}
	return reader.DBTX.QueryRowContext(ctx, query, arguments...)
}

func TestUncachedChildCleanupChecksTheActualRowBeforeItRemovesActivity(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"missing row", "read failure", "foreign parent", "missing activity", "missing queries"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			svc, parent, childID := setupRunningSubagent(t, "uncached-child-root", bgtask.StatusRunning)
			root := requireRootOutputSink(t, svc.Output, "uncached-child-root")
			root.childMu.Lock()
			cached := root.childSinks[childID]
			root.childMu.Unlock()
			require.Nil(t, cached)
			if boundary == "foreign parent" {
				parent = newRootAgent(t, svc, "another-root")
			}
			if boundary == "missing activity" {
				svc.Output.ForgetActivity(childID)
			}
			beforeActivity, activityPresent := svc.Output.activity.Load(childID)
			tracker := svc.Output.childTracker(childID)
			var reads atomic.Int32
			svc.Output.WaitActivityRefreshes()
			originalQueries := svc.Output.queries
			defer func() { svc.Output.queries = originalQueries }()
			svc.Output.queries = db.New(observedChildRowRead{DBTX: svc.DB, childID: childID, read: func(ctx context.Context, query string, arguments ...any) *sql.Row {
				reads.Add(1)
				switch boundary {
				case "missing row":
					return svc.DB.QueryRowContext(ctx, "SELECT 1 WHERE 0")
				case "read failure":
					return svc.DB.QueryRowContext(ctx, "SELECT nonexistent_child_column")
				default:
					return svc.DB.QueryRowContext(ctx, query, arguments...)
				}
			}})
			if boundary == "missing queries" {
				svc.Output.queries = nil
			}
			parent.CleanupChildAgent(childID)
			if boundary == "missing queries" {
				assert.Zero(t, reads.Load())
			} else {
				assert.Equal(t, int32(1), reads.Load(), "the direct-parent check must use the actual row")
			}
			afterActivity, afterPresent := svc.Output.activity.Load(childID)
			assert.Equal(t, activityPresent, afterPresent)
			if activityPresent {
				assert.Same(t, beforeActivity, afterActivity)
			}
			afterTracker, _, present := svc.Output.trackers.get(childID)
			assert.True(t, present)
			assert.Same(t, tracker, afterTracker)
			assert.Nil(t, svc.Output.sinkForAgent(childID), "refused cleanup must not create a child sink")
		})
	}
}

func TestUncachedChildCleanupPreservesAReplacementDuringExternalPreparation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	svc, parent, childID := setupRunningSubagent(t, "uncached-replacement-root", bgtask.StatusRunning)
	root := requireRootOutputSink(t, svc.Output, "uncached-replacement-root")
	parent.SetTurnState(agent.TurnState{Active: true}, 1)
	holdSettles(t, svc.Output)
	require.NoError(t, parent.CloseBackgroundTask("task-1", bgtask.StatusSucceeded))
	value, present := svc.Output.activity.Load(childID)
	require.True(t, present)
	activity := value.(*agentActivity)
	activity.mu.Lock()
	settlePending := activity.settlePending
	activity.mu.Unlock()
	require.True(t, settlePending)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finishPreparation := func() { releaseOnce.Do(func() { close(release) }) }
	var next atomic.Bool
	next.Store(true)
	svc.Output.processRunning = func(string) bool {
		if next.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return true
	}
	removed := make(chan struct{})
	go func() { parent.CleanupChildAgent(childID); close(removed) }()
	defer func() { finishPreparation(); <-removed }()
	select {
	case <-entered:
	case <-removed:
		t.Fatal("uncached cleanup returned before its held-settle preparation")
	case <-ctx.Done():
		t.Fatal("uncached cleanup did not reach its external preparation")
	}
	services := parent.ChildSink(childID)
	child := requireChildOutputSink(t, root, childID)
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "uncached-replacement-wire"}, onEvent: func(*leapmuxv1.AgentEvent) {}}
	registerAgentWatch(svc, writer.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	queued := make(chan struct{})
	var queuedOnce sync.Once
	publish := child.progress.publish
	child.progress.publish = func(info map[string]interface{}) {
		publish(info)
		if thinking, valid := info[contracts.SessionInfoKeyThinkingTokens].(int64); valid && thinking == 23 {
			queuedOnce.Do(func() { close(queued) })
		}
	}
	services.UpdateSessionID("replacement-native")
	services.SetTurnState(agent.TurnState{Active: true}, 2)
	services.ReportProgress(agent.NativeTokenProgress("replacement-model", 23))
	stopCapturedProgressTimers(child.progress)
	child.progress.flush()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("replacement progress did not reach its actual sender")
	}
	_, err := services.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"system","text":"replacement notice"}`)})
	require.NoError(t, err)
	thread, present := svc.Output.lastNotifThread.Load(childID)
	require.True(t, present)
	finishPreparation()
	select {
	case <-removed:
	case <-ctx.Done():
		t.Fatal("uncached cleanup did not finish after its preparation released")
	}
	assert.Same(t, child, svc.Output.sinkForAgent(childID))
	retained, present := svc.Output.lastNotifThread.Load(childID)
	require.True(t, present)
	assert.Same(t, thread, retained)
	assert.Equal(t, int64(23), child.progress.snapshotInfo()[contracts.SessionInfoKeyThinkingTokens])
	var latestRevision uint64
	var latestThinking int64
	for _, event := range decodeAgentEvents(writer.testResponseWriter) {
		info, err := capturedTailSessionInfo(event)
		require.NoError(t, err)
		if encoded, present := info[contracts.SessionInfoKeyGenerationProgressRevision]; present {
			var revision uint64
			var thinking int64
			require.NoError(t, json.Unmarshal(encoded, &revision))
			require.NoError(t, json.Unmarshal(info[contracts.SessionInfoKeyThinkingTokens], &thinking))
			if revision >= latestRevision {
				latestRevision, latestThinking = revision, thinking
			}
		}
	}
	assert.Positive(t, latestRevision)
	assert.Equal(t, int64(23), latestThinking)
}

func TestActivityRemovalDeliversASettleThatStartsAfterPreparation(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	handler, recorder := newActivityHandler(t, "agent-1")
	settles := holdSettles(t, handler)
	handler.setTurnActive("agent-1", "agent-1", true)
	require.Equal(t, []bool{true}, recorder.busyStates())
	entered, releaseInputs := make(chan struct{}), make(chan struct{})
	var releaseInputsOnce sync.Once
	releaseProvider := func() { releaseInputsOnce.Do(func() { close(releaseInputs) }) }
	var blockNext atomic.Bool
	blockNext.Store(true)
	handler.processRunning = func(string) bool {
		if blockNext.CompareAndSwap(true, false) {
			close(entered)
			<-releaseInputs
		}
		return true
	}
	ended := make(chan struct{})
	go func() {
		handler.setTurnActive("agent-1", "agent-1", false)
		close(ended)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		releaseProvider()
		<-ended
		t.Fatal("the turn-end refresh did not reach its external inputs")
	}
	mutation := handler.transcriptMutationMutex("agent-1")
	mutation.RLock()
	var releaseMutationOnce sync.Once
	releaseMutation := func() { releaseMutationOnce.Do(mutation.RUnlock) }
	removed := make(chan struct{})
	go func() { handler.ForgetActivity("agent-1"); close(removed) }()
	defer func() {
		releaseProvider()
		releaseMutation()
		<-ended
		<-removed
		handler.CancelHeldSettles()
	}()
	// A pending writer prevents another reader while the retained reader still holds the lease.
	// This observes removal's lease request after its read-only preparation, without a timed window.
	require.Eventually(t, func() bool {
		if mutation.TryRLock() {
			mutation.RUnlock()
			return false
		}
		return true
	}, 30*time.Second, time.Millisecond)
	releaseProvider()
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("the turn-end refresh did not finish after its inputs released")
	}
	require.Len(t, settles.openWindows(), 1)
	releaseMutation()
	select {
	case <-removed:
	case <-ctx.Done():
		t.Fatal("activity removal did not finish after its mutation lease released")
	}
	assert.Equal(t, []bool{true, false}, recorder.busyStates())
	assert.Empty(t, settles.openWindows())
	_, retained := handler.activity.Load("agent-1")
	assert.False(t, retained)
}

func TestActivityRemovalPreparesAgainAfterANewerHeldSettleTicket(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	handler, recorder := newActivityHandler(t, "ticket-owner")
	settles := holdSettles(t, handler)
	handler.setTurnActive("ticket-owner", "ticket-owner", true)
	handler.setTurnActive("ticket-owner", "ticket-owner", false)
	require.Len(t, settles.openWindows(), 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseInputs := func() { releaseOnce.Do(func() { close(release) }) }
	var reads atomic.Int32
	handler.processRunning = func(string) bool {
		if reads.Add(1) == 1 {
			close(entered)
			<-release
		}
		return true
	}
	completed := make(chan struct{})
	go func() { handler.ForgetActivity("ticket-owner"); close(completed) }()
	defer func() { releaseInputs(); <-completed; handler.CancelHeldSettles() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("activity removal did not reach its original held-settle preparation")
	}
	handler.refreshActivity("ticket-owner", "ticket-owner")
	require.Len(t, settles.openWindows(), 1)
	assert.Equal(t, []bool{true}, recorder.busyStates())
	releaseInputs()
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("activity removal did not finish after its preparation released")
	}
	assert.Equal(t, int32(3), reads.Load(), "the newer refresh requires another external preparation")
	assert.Equal(t, []bool{true, false}, recorder.busyStates(), "removal must deliver the held settle after a newer ticket")
	assert.Empty(t, settles.openWindows())
	_, retained := handler.activity.Load("ticket-owner")
	assert.False(t, retained)
}

func TestActivityRemovalRetainsControlMutationsBeforeAndAfterPreparation(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"before preparation", "after preparation"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			handler, recorder := newActivityHandler(t, "control-retention-owner")
			settles := holdSettles(t, handler)
			handler.setTurnActive("control-retention-owner", "control-retention-owner", true)
			handler.setTurnActive("control-retention-owner", "control-retention-owner", false)
			windows := settles.openWindows()
			require.Len(t, windows, 1)
			before, present := handler.activity.Load("control-retention-owner")
			require.True(t, present)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseInputs := func() { releaseOnce.Do(func() { close(release) }) }
			var reads atomic.Int32
			handler.processRunning = func(string) bool {
				if reads.Add(1) == 1 && boundary == "after preparation" {
					close(entered)
					<-release
				}
				return true
			}
			completed := make(chan struct{})
			var finishControl func()
			var finishControlOnce sync.Once
			releaseControl := func() {
				finishControlOnce.Do(func() {
					if finishControl != nil {
						finishControl()
					}
				})
			}
			if boundary == "before preparation" {
				_, _, finishControl = handler.lockControlMutation("control-retention-owner", "control-retention-owner")
			}
			go func() { handler.ForgetActivity("control-retention-owner"); close(completed) }()
			defer func() { releaseInputs(); releaseControl(); <-completed; handler.CancelHeldSettles() }()
			if boundary == "after preparation" {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("activity removal did not reach its external preparation")
				}
				_, _, finishControl = handler.lockControlMutation("control-retention-owner", "control-retention-owner")
				releaseInputs()
			}
			select {
			case <-completed:
			case <-ctx.Done():
				t.Fatal("activity removal waited for a retained control mutation")
			}
			after, present := handler.activity.Load("control-retention-owner")
			require.True(t, present)
			assert.Same(t, before, after)
			remaining := settles.openWindows()
			require.Len(t, remaining, 1)
			assert.Same(t, windows[0], remaining[0])
			assert.Equal(t, []bool{true}, recorder.busyStates())
			if boundary == "before preparation" {
				assert.Zero(t, reads.Load(), "a retained entry must refuse before external preparation")
			} else {
				assert.Equal(t, int32(1), reads.Load())
			}
			releaseControl()
			handler.ForgetActivity("control-retention-owner")
			assert.Equal(t, []bool{true, false}, recorder.busyStates())
			assert.Empty(t, settles.openWindows())
			_, retained := handler.activity.Load("control-retention-owner")
			assert.False(t, retained)
		})
	}
}

func TestChildRetirementWatcherPreservesReplacementProgressOnTheWire(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, parent := setupRootSink(t, "retirement-reentry-root")
	svc.Output.processRunning = func(string) bool { return true }
	root := requireRootOutputSink(t, svc.Output, "retirement-reentry-root")
	childID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: "child-native", Title: "Child"})
	require.NoError(t, err)
	childServices := parent.ChildSink(childID)
	child := requireChildOutputSink(t, root, childID)
	childServices.UpdateSessionID("original-native")
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "child-retirement-reentry"}}
	var once sync.Once
	result := make(chan error, 1)
	var replacement *agentOutputSink
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		activity := event.GetActivityChanged()
		if event.AgentId != childID || activity == nil || activity.State != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE {
			return
		}
		once.Do(func() {
			services := root.ChildSink(childID)
			root.childMu.Lock()
			replacement = root.childSinks[childID]
			root.childMu.Unlock()
			if replacement == nil {
				result <- errors.New("the watcher did not register a replacement child")
				return
			}
			queued := make(chan struct{})
			var queuedOnce sync.Once
			publish := replacement.progress.publish
			// Set the callback before this publisher receives its first progress report.
			replacement.progress.publish = func(info map[string]interface{}) {
				publish(info)
				queuedOnce.Do(func() { close(queued) })
			}
			services.UpdateSessionID("replacement-native")
			services.ReportProgress(agent.NativeTokenProgress("replacement-model", 23))
			replacement.progress.flush()
			select {
			case <-queued:
				result <- nil
			case <-ctx.Done():
				result <- errors.New("the replacement progress did not enter the publication queue")
			}
		})
	}
	registerAgentWatch(svc, writer.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	childServices.SetTurnState(agent.TurnState{Active: true}, 1)
	require.NoError(t, parent.CloseBackgroundTask("child-native", bgtask.StatusSucceeded))
	activity, exists := svc.Output.activity.Load(childID)
	require.True(t, exists)
	activity.(*agentActivity).mu.Lock()
	pending := activity.(*agentActivity).settlePending
	activity.(*agentActivity).mu.Unlock()
	require.True(t, pending)
	completed := make(chan struct{})
	go func() { root.CleanupChildAgent(childID); close(completed) }()
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("child retirement blocked during watcher reentry")
	}
	require.NoError(t, <-result)
	require.NotNil(t, replacement)
	assert.NotSame(t, child, replacement)
	assert.Same(t, replacement, svc.Output.sinkForAgent(childID))
	assert.Equal(t, "replacement-native", replacement.currentMessageSessionID())
	assert.Equal(t, int64(23), replacement.progress.snapshotInfo()[contracts.SessionInfoKeyThinkingTokens])
	var latestRevision uint64
	var latestThinking int64
	var receivedPositive bool
	for _, stream := range writer.streamsSnapshot() {
		event := decodeWatchAgentEvent(t, stream)
		message := event.GetAgentMessage()
		if event.AgentId != childID || message == nil || message.Seq >= 0 {
			continue
		}
		content, err := msgcodec.Decompress(message.Content, message.ContentCompression)
		require.NoError(t, err)
		var envelope struct {
			Info map[string]json.RawMessage `json:"info"`
		}
		require.NoError(t, json.Unmarshal(content, &envelope))
		encoded, present := envelope.Info[contracts.SessionInfoKeyGenerationProgressRevision]
		if !present {
			continue
		}
		var revision uint64
		var thinking int64
		require.NoError(t, json.Unmarshal(encoded, &revision))
		require.NoError(t, json.Unmarshal(envelope.Info[contracts.SessionInfoKeyThinkingTokens], &thinking))
		if thinking == 23 {
			receivedPositive = true
		}
		if revision > latestRevision {
			latestRevision, latestThinking = revision, thinking
		}
	}
	assert.True(t, receivedPositive)
	assert.Equal(t, int64(23), latestThinking, "a retirement clear must not carry a later revision than replacement progress")
}
