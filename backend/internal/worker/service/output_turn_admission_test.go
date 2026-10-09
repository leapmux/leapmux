package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestProviderTurnAdmissionRejectsAnOldEndedHandlerAfterANewerActiveHandler(t *testing.T) {
	t.Parallel()
	for _, replacementProcess := range []bool{false, true} {
		t.Run(map[bool]string{false: "later turn", true: "replacement process"}[replacementProcess], func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseHandler()
			original := svc.Output.turnState
			svc.Output.SetTurnStateFunc(func(agentID string, admission agent.TurnStateAdmission) {
				state, lease := admission.Acquire()
				if lease == nil {
					return
				}
				lease.Release()
				if !state.Active {
					close(entered)
					<-release
				}
				original(agentID, admission)
			})
			ended := make(chan struct{})
			go func() {
				sink.SetTurnState(agent.TurnState{}, 2)
				close(ended)
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the ended handler did not reach its delayed queue admission")
			}
			if replacementProcess {
				replacement := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				replacement.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 1)
			} else {
				sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 3)
			}
			before, err := svc.InputQueue.Snapshot(ctx, ownerID)
			require.NoError(t, err)
			require.True(t, before.ActiveTurn)
			releaseHandler()
			select {
			case <-ended:
			case <-ctx.Done():
				t.Fatal("the delayed ended handler did not finish")
			}
			after, err := svc.InputQueue.Snapshot(ctx, ownerID)
			require.NoError(t, err)
			assert.Equal(t, before, after, "an old ended handler must not mutate a newer queue turn")
		})
	}
}

type heldChildSessionStore struct {
	db.DBTX
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type rootSessionReadSignal struct {
	*heldChildSessionStore
	reads   atomic.Int32
	entered chan struct{}
}

func (store *rootSessionReadSignal) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := store.DBTX.QueryRowContext(ctx, query, args...)
	if strings.Contains(query, "-- name: GetAgentByID :one") && store.reads.Add(1) == 2 {
		close(store.entered)
	}
	return row
}

func TestRootSessionSetterSerializesNewSinkSessionRestoration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	svc, parent := setupRootSink(t, "root-held-session-restoration")
	root := requireRootOutputSink(t, svc.Output, "root-held-session-restoration")
	parent.UpdateSessionID("root-original")
	captured := agent.CaptureTranscript(parent, agent.MessageContent{Original: []byte(`{"text":"old geometry"}`)}, agent.SpanInfo{})
	releaseStore := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStore) }) }
	defer release()
	store := &rootSessionReadSignal{heldChildSessionStore: &heldChildSessionStore{DBTX: svc.DB, entered: make(chan struct{}), release: releaseStore}, entered: make(chan struct{})}
	svc.Output.queries = db.New(store)
	setterFinished := make(chan struct{})
	go func() { root.UpdateSessionID("root-admitted"); close(setterFinished) }()
	select {
	case <-store.heldChildSessionStore.entered:
	case <-ctx.Done():
		t.Fatal("the root setter did not reach its held database write")
	}
	replacementFinished := make(chan agent.ProviderServices, 1)
	go func() { replacementFinished <- svc.Output.NewSink(root.agentID, root.agentProvider) }()
	readCrossed := false
	select {
	case <-store.entered:
		readCrossed = true
	case <-time.After(30 * time.Second):
	}
	release()
	select {
	case <-setterFinished:
	case <-ctx.Done():
		t.Fatal("the root setter did not finish after its database write released")
	}
	select {
	case <-replacementFinished:
	case <-ctx.Done():
		t.Fatal("the root replacement did not finish after the setter released")
	}
	assert.False(t, readCrossed, "a replacement must not restore a session before the admitted setter commits")
	current := requireRootOutputSink(t, svc.Output, root.agentID)
	row, err := svc.Queries.GetAgentByID(ctx, root.agentID)
	require.NoError(t, err)
	assert.Equal(t, "root-admitted", row.AgentSessionID)
	assert.Equal(t, row.AgentSessionID, current.currentMessageSessionID())
	require.NoError(t, captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	message, err := svc.Queries.GetLatestMessageByAgentID(ctx, root.agentID)
	require.NoError(t, err)
	assert.Equal(t, "root-original", message.AgentSessionID)
	assert.True(t, message.TranscriptOnly)
}

func (store *heldChildSessionStore) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "-- name: UpdateAgentSessionID :exec") {
		store.once.Do(func() {
			close(store.entered)
			<-store.release
		})
	}
	return store.DBTX.ExecContext(ctx, query, args...)
}

func TestChildSessionSetterSerializesChildRetirementAndReplacement(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	svc, parent := setupRootSink(t, "root-held-child-session")
	root := requireRootOutputSink(t, svc.Output, "root-held-child-session")
	childID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "held-child", ProviderChildKey: "held-native-child", Title: "Child"})
	require.NoError(t, err)
	child := requireChildOutputSink(t, root, childID)
	child.UpdateSessionID("child-original")
	releaseStore := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStore) }) }
	defer release()
	store := &heldChildSessionStore{DBTX: svc.DB, entered: make(chan struct{}), release: releaseStore}
	svc.Output.queries = db.New(store)
	setterFinished := make(chan struct{})
	go func() { child.UpdateSessionID("child-admitted"); close(setterFinished) }()
	select {
	case <-store.entered:
	case <-ctx.Done():
		t.Fatal("the child setter did not reach its held database write")
	}
	mutation := svc.Output.transcriptMutationMutex(root.agentID)
	if mutation.TryLock() {
		mutation.Unlock()
		t.Fatal("the child setter did not hold its root mutation lease")
	}
	replacementFinished := make(chan *agentOutputSink, 1)
	go func() {
		root.CleanupChildAgent(childID)
		root.ChildSink(childID)
		root.childMu.Lock()
		replacement := root.childSinks[childID]
		root.childMu.Unlock()
		replacementFinished <- replacement
	}()
	var replacement *agentOutputSink
	crossed := false
	// This guard releases the held setter after the test checks the competing operation.
	// It does not advance a provider timer or determine an activity state.
	select {
	case replacement = <-replacementFinished:
		crossed = true
	case <-time.After(30 * time.Second):
	}
	release()
	select {
	case <-setterFinished:
	case <-ctx.Done():
		t.Fatal("the child setter did not finish after its database write released")
	}
	if replacement == nil {
		select {
		case replacement = <-replacementFinished:
		case <-ctx.Done():
			t.Fatal("the child replacement did not finish after the root lease released")
		}
	}
	assert.False(t, crossed, "a child replacement must not cross an admitted session write")
	require.NotNil(t, replacement)
	assert.NotSame(t, child, replacement)
	assert.Same(t, replacement, svc.Output.sinkForAgent(childID))
	assert.Equal(t, "child-admitted", replacement.currentMessageSessionID())
	replacement.UpdateSessionID("child-replacement")
	child.UpdateSessionID("child-obsolete")
	row, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, "child-replacement", row.AgentSessionID)
}

func TestProviderTurnAdmissionRetainsTheScopeWhenOnlyTheTurnKindChanges(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	captured := sink.CaptureMessage(agent.MessageContent{Original: []byte(`{"message":"same turn"}`)}, agent.SpanInfo{})
	sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 2)
	assert.True(t, captured.Publication.Owner().IsCurrent())
	snapshot, err := svc.InputQueue.Snapshot(ctx, ownerID)
	require.NoError(t, err)
	assert.True(t, snapshot.ActiveTurn)
	assert.True(t, snapshot.ActiveTurnSteerable)
}

type turnAdmissionWatchingWriter struct {
	*testResponseWriter
	onEvent func(*leapmuxv1.AgentEvent)
}

func (writer *turnAdmissionWatchingWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	if err := writer.testResponseWriter.SendStream(message); err != nil {
		return err
	}
	var response leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &response); err != nil {
		return err
	}
	if event := response.GetAgentEvent(); event != nil {
		writer.onEvent(event)
	}
	return nil
}

func TestProviderTurnAdmissionWatcherCanPublishTheNextTurnSynchronously(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	var once sync.Once
	observerResult := make(chan error, 1)
	nestedFinished := make(chan struct{})
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "turn-admission-reentry"}}
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if event.GetInputQueueChanged() == nil {
			return
		}
		invoke := false
		once.Do(func() { invoke = true })
		if invoke {
			go func() {
				sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 2)
				close(nestedFinished)
			}()
			select {
			case <-nestedFinished:
				observerResult <- nil
			case <-time.After(30 * time.Second):
				observerResult <- errors.New("the queue watcher holds the coordinator lock across turn reentry")
			}
		}
	}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	completed := make(chan struct{})
	go func() {
		sink.SetTurnState(agent.TurnState{Active: true}, 1)
		close(completed)
	}()
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("the queue watcher blocked when it published the next turn")
	}
	select {
	case <-nestedFinished:
	case <-ctx.Done():
		t.Fatal("the queue watcher did not reconcile the next turn")
	}
	assert.NoError(t, <-observerResult)
	snapshot, err := svc.InputQueue.Snapshot(ctx, ownerID)
	require.NoError(t, err)
	assert.True(t, snapshot.ActiveTurnSteerable)
}

func TestObsoleteSinkTurnReportPreservesActivityBeforeAndAfterAdoption(t *testing.T) {
	t.Parallel()
	for _, adopted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before adoption", true: "after adoption"}[adopted], func(t *testing.T) {
			t.Parallel()
			svc, oldSink, ownerID, _ := setupBgTaskTestWithService(t)
			oldSink.SetTurnState(agent.TurnState{Active: true}, 1)
			replacement := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			if adopted {
				svc.Output.NoteAgentProcessStarted(ownerID)
				replacement.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 1)
			} else {
				_, present := svc.Output.turnPublisher.Load(ownerID)
				require.False(t, present)
			}
			activity := svc.Output.activityFor(ownerID, ownerID)
			activity.mu.Lock()
			priorActive, priorScope, priorSeq := activity.turnActive, activity.scope, activity.turnSeq
			activity.mu.Unlock()
			before, err := svc.InputQueue.Snapshot(t.Context(), ownerID)
			require.NoError(t, err)
			oldSink.SetTurnState(agent.TurnState{}, 99)
			activity.mu.Lock()
			assert.Equal(t, priorActive, activity.turnActive)
			assert.Same(t, priorScope, activity.scope)
			assert.Equal(t, priorSeq, activity.turnSeq)
			activity.mu.Unlock()
			after, err := svc.InputQueue.Snapshot(t.Context(), ownerID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestObsoleteSinkSessionSetterPreservesTheReplacementSession(t *testing.T) {
	t.Parallel()
	for _, adopted := range []bool{false, true} {
		for _, returnsToOriginal := range []bool{false, true} {
			label := map[bool]string{false: "before adoption", true: "after adoption"}[adopted]
			if returnsToOriginal {
				label += " A-B-A"
			}
			t.Run(label, func(t *testing.T) {
				t.Parallel()
				svc, oldSink, ownerID, _ := setupBgTaskTestWithService(t)
				oldSink.UpdateSessionID("session-a")
				captured := agent.CaptureTranscript(oldSink, agent.MessageContent{Original: []byte(`{"type":"assistant","text":"original"}`)}, agent.SpanInfo{})
				replacement := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				replacement.UpdateSessionID("session-b")
				if returnsToOriginal {
					replacement.UpdateSessionID("session-a")
				}
				if adopted {
					svc.Output.NoteAgentProcessStarted(ownerID)
				}
				current := requireRootOutputSink(t, svc.Output, ownerID)
				priorFact := current.currentMessageSessionFact()
				before, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
				require.NoError(t, err)
				oldSink.UpdateSessionID("obsolete-session-report")
				after, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
				require.NoError(t, err)
				assert.Equal(t, before.AgentSessionID, after.AgentSessionID)
				assert.Same(t, priorFact, current.currentMessageSessionFact())
				require.NoError(t, captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
				row, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
				require.NoError(t, err)
				assert.Equal(t, "session-a", row.AgentSessionID)
				assert.True(t, row.TranscriptOnly)
			})
		}
	}
}
