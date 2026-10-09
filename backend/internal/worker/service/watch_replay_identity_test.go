package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type replayIdentityWriter struct {
	*testResponseWriter
	onBind func(channel.StreamController)
	onSend func(*leapmuxv1.InnerStreamMessage)
}

func (writer *replayIdentityWriter) BindStream(controller channel.StreamController) (func(), bool) {
	release, bound := writer.testResponseWriter.BindStream(controller)
	if bound && writer.onBind != nil {
		writer.onBind(controller)
	}
	return release, bound
}

func (writer *replayIdentityWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	if err := writer.testResponseWriter.SendStream(message); err != nil {
		return err
	}
	if writer.onSend != nil {
		writer.onSend(message)
	}
	return nil
}

func replayIdentityRequest(updateID, replayID uint64, agentID string, cursor int64) *leapmuxv1.WatchEventsRequest {
	tail := cursor
	return &leapmuxv1.WatchEventsRequest{UpdateId: updateID, Agents: []*leapmuxv1.WatchAgentEntry{{
		AgentId: agentID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: replayID,
		Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: cursor, WindowTailSeq: &tail,
	}}}
}

func openReplayIdentityWatch(t *testing.T, dispatcher *channel.Dispatcher, request *leapmuxv1.WatchEventsRequest, writer *replayIdentityWriter) {
	t.Helper()
	payload, err := proto.Marshal(request)
	require.NoError(t, err)
	dispatcher.DispatchWith(t.Context(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{Method: "WatchEvents", Payload: payload}, writer)
	t.Cleanup(func() { writer.deliverStreamRequest(nil, true); waitStreamEnded(t, writer.testResponseWriter) })
}

func reviseReplayIdentityWatch(t *testing.T, writer *testResponseWriter, request *leapmuxv1.WatchEventsRequest) {
	t.Helper()
	payload, err := proto.Marshal(request)
	require.NoError(t, err)
	writer.deliverStreamRequest(payload, false)
}

func createReplayIdentityRows(t *testing.T, svc *Service, agentID string) []db.Message {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	for _, text := range []string{"first", "second", "third"} {
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant","text":"` + text + `"}`)}, agent.SpanInfo{}))
	}
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	return rows
}

func waitReplayIdentityAck(t *testing.T, writer *testResponseWriter, updateID uint64) *leapmuxv1.WatchUpdateAck {
	t.Helper()
	var ack *leapmuxv1.WatchUpdateAck
	require.Eventually(t, func() bool { ack = lastWatchUpdateAck(t, writer); return ack != nil && ack.UpdateId == updateID }, 30*time.Second, time.Millisecond)
	return ack
}

func assertReplayIdentityFrames(t *testing.T, writer *testResponseWriter, agentID string, replayID uint64, expected []db.Message) {
	t.Helper()
	var ids []string
	for _, event := range decodeAgentEvents(writer) {
		if !event.Replay || event.AgentId != agentID {
			continue
		}
		assert.Equal(t, replayID, event.ReplayId)
		assert.Equal(t, agentID, readReplayOrigin(event))
		if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
			ids = append(ids, message.Id)
		}
	}
	want := make([]string, len(expected))
	for i, row := range expected {
		want[i] = row.ID
	}
	assert.Equal(t, want, ids)
}

// readReplayOrigin reads the received generated field through the current schema.
// An absent field returns an empty value so the old API produces a behavioral failure.
func readReplayOrigin(event *leapmuxv1.AgentEvent) string {
	if event == nil {
		return ""
	}
	message := event.ProtoReflect()
	field := message.Descriptor().Fields().ByName("replay_agent_id")
	if field == nil || field.Kind() != protoreflect.StringKind {
		return ""
	}
	return message.Get(field).String()
}

func createReplayOriginTree(t *testing.T, svc *Service, rootID string, childKeys ...string) (agent.ProviderServices, []string) {
	t.Helper()
	root := newRootAgent(t, svc, rootID)
	root.UpdateSessionID("root-native-session")
	root.UpsertGoal(agent.GoalUpdate{NativeID: "root-native-goal", Objective: "Keep the root goal", Status: agent.GoalStatusActive, Snapshot: true})
	require.NoError(t, root.UpsertBackgroundTask(bgtask.Upsert{RowKey: "root-shell", Kind: bgtask.KindShell, Title: "Root shell", Status: bgtask.StatusRunning}))
	children := make([]string, 0, len(childKeys))
	for _, key := range childKeys {
		childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn-" + key, ProviderChildKey: key, Title: key, AgentSessionID: "native-" + key})
		require.NoError(t, err)
		child := root.ChildSink(childID)
		child.UpdateSessionID("native-" + key)
		require.NoError(t, child.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant","text":"` + key + `"}`)}, agent.SpanInfo{}))
		children = append(children, childID)
	}
	return root, children
}

func replayOriginEntry(agentID string, identity uint64) *leapmuxv1.WatchAgentEntry {
	return &leapmuxv1.WatchAgentEntry{AgentId: agentID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, ReplayId: identity}
}

func assertRootReplayOrigins(t *testing.T, writer *testResponseWriter, rootID string, identities map[string]uint64) {
	t.Helper()
	var origin string
	projections := make(map[string]int, len(identities))
	messages := make(map[string]int, len(identities))
	for _, event := range decodeAgentEvents(writer) {
		if !event.GetReplay() {
			continue
		}
		if event.GetCatchUpStart() != nil {
			origin = event.GetAgentId()
		}
		require.Contains(t, identities, origin, "each replay burst must start with its actual watched agent")
		assert.Equal(t, identities[origin], event.GetReplayId())
		assert.Equal(t, origin, readReplayOrigin(event), "the received origin must identify the watched agent even for root projections")
		if task := event.GetBackgroundTasksChanged(); task != nil {
			assert.Equal(t, rootID, event.GetAgentId())
			assert.Equal(t, rootID, task.GetAgentId())
			var found bool
			for _, item := range task.GetTasks() {
				found = found || item.GetId() == "root-shell"
			}
			assert.True(t, found, "the actual root registry row must reach the child replay")
			projections[origin]++
		}
		if goal := event.GetGoalChanged(); goal != nil {
			assert.Equal(t, rootID, event.GetAgentId())
			assert.Equal(t, rootID, goal.GetAgentId())
			assert.Equal(t, "Keep the root goal", goal.GetGoal().GetObjective())
			projections[origin]++
		}
		if message := event.GetAgentMessage(); message != nil && message.GetSeq() > 0 {
			assert.Equal(t, origin, event.GetAgentId())
			messages[origin]++
		}
		if event.GetCatchUpComplete() != nil {
			assert.Equal(t, origin, event.GetAgentId())
			origin = ""
		}
	}
	assert.Empty(t, origin, "every actual replay burst must complete")
	for agentID := range identities {
		assert.Equal(t, 2, projections[agentID], "each watched agent receives both root projections")
		if agentID != rootID {
			assert.Equal(t, 1, messages[agentID], "the real child transcript remains under its own destination")
		}
	}
}

func TestWatchReplayIdentityKeepsChildOriginForRootSnapshots(t *testing.T) {
	t.Parallel()
	for _, rootMode := range []leapmuxv1.WatchMode{leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, leapmuxv1.WatchMode_WATCH_MODE_FULL} {
		t.Run(rootMode.String(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, base := setupTestService(t)
			const rootID = "replay-root"
			_, children := createReplayOriginTree(t, svc, rootID, "child")
			const childIdentity = uint64(9007199254740993)
			rootEntry := &leapmuxv1.WatchAgentEntry{AgentId: rootID, Mode: rootMode}
			identities := map[string]uint64{children[0]: childIdentity}
			if rootMode == leapmuxv1.WatchMode_WATCH_MODE_FULL {
				rootEntry = replayOriginEntry(rootID, ^uint64(0))
				identities[rootID] = rootEntry.GetReplayId()
			}
			openReplayIdentityWatch(t, dispatcher, &leapmuxv1.WatchEventsRequest{UpdateId: 19, Agents: []*leapmuxv1.WatchAgentEntry{rootEntry, replayOriginEntry(children[0], childIdentity)}}, &replayIdentityWriter{testResponseWriter: base})
			ack := waitReplayIdentityAck(t, base, 19)
			require.Eventually(t, func() bool { return countCatchUpCompletes(base) == len(identities) }, 30*time.Second, time.Millisecond)
			require.Len(t, ack.GetAgentStates(), 2)
			assertRootReplayOrigins(t, base, rootID, identities)
		})
	}
}

func TestWatchReplayIdentityDistinguishesSiblingOriginsWithOneLifetime(t *testing.T) {
	t.Parallel()
	for _, identity := range []uint64{9007199254740993, ^uint64(0)} {
		t.Run(fmt.Sprint(identity), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, base := setupTestService(t)
			const rootID = "sibling-replay-root"
			root, children := createReplayOriginTree(t, svc, rootID, "first-child", "second-child")
			request := &leapmuxv1.WatchEventsRequest{UpdateId: 23, Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: rootID, Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}, replayOriginEntry(children[0], identity), replayOriginEntry(children[1], identity)}}
			openReplayIdentityWatch(t, dispatcher, request, &replayIdentityWriter{testResponseWriter: base})
			ack := waitReplayIdentityAck(t, base, 23)
			require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 2 }, 30*time.Second, time.Millisecond)
			require.Len(t, ack.GetAgentStates(), 3)
			assertRootReplayOrigins(t, base, rootID, map[string]uint64{children[0]: identity, children[1]: identity})
			// Live goals require FULL interest. Keep both sibling lifetimes unchanged.
			liveInterest := proto.Clone(request).(*leapmuxv1.WatchEventsRequest)
			liveInterest.UpdateId = 24
			liveInterest.Agents[0] = replayOriginEntry(rootID, identity)
			reviseReplayIdentityWatch(t, base, liveInterest)
			waitReplayIdentityAck(t, base, 24)
			require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 3 }, 30*time.Second, time.Millisecond)
			before := len(decodeAgentEvents(base))
			root.UpsertGoal(agent.GoalUpdate{NativeID: "later-root-goal", Objective: "A live root goal", Status: agent.GoalStatusActive})
			require.NoError(t, root.UpsertBackgroundTask(bgtask.Upsert{RowKey: "later-root-shell", Kind: bgtask.KindShell, Title: "Later root shell", Status: bgtask.StatusRunning}))
			var topics int
			for _, event := range decodeAgentEvents(base)[before:] {
				if event.GetGoalChanged() == nil && event.GetBackgroundTasksChanged() == nil {
					continue
				}
				topics++
				assert.Equal(t, rootID, event.GetAgentId())
				assert.False(t, event.GetReplay())
				assert.Zero(t, event.GetReplayId())
				assert.Empty(t, readReplayOrigin(event))
			}
			assert.Equal(t, 2, topics)
		})
	}
}

func TestWatchReplayIdentitySurvivesSkippedOpeningRequest(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	const agentID = "skipped-opening-agent"
	rows := createReplayIdentityRows(t, svc, agentID)
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "other-notify-agent", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	first := replayIdentityRequest(1, 1, agentID, rows[0].Seq)
	second := proto.Clone(first).(*leapmuxv1.WatchEventsRequest)
	second.UpdateId = 2
	second.Agents = append(second.Agents, &leapmuxv1.WatchAgentEntry{AgentId: "other-notify-agent", Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY})
	payload, err := proto.Marshal(second)
	require.NoError(t, err)
	writer := &replayIdentityWriter{testResponseWriter: base, onBind: func(controller channel.StreamController) { controller.OnClientFrame(payload) }}
	openReplayIdentityWatch(t, dispatcher, first, writer)
	ack := waitReplayIdentityAck(t, base, 2)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
	assertReplayIdentityFrames(t, base, agentID, 1, rows[1:])
	require.Len(t, ack.AgentStates, 2)
	states := map[string]*leapmuxv1.WatchAgentState{}
	for _, state := range ack.AgentStates {
		states[state.AgentId] = state
	}
	require.Contains(t, states, agentID)
	assert.Equal(t, uint64(1), states[agentID].ReplayId)
	assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, states[agentID].Mode)
	require.Contains(t, states, "other-notify-agent")
	assert.Zero(t, states["other-notify-agent"].ReplayId)
}

func TestWatchReplayIdentityDoesNotRepeatAnActualUnchangedLifetime(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	const agentID = "unchanged-lifetime-agent"
	rows := createReplayIdentityRows(t, svc, agentID)
	writer := &replayIdentityWriter{testResponseWriter: base}
	openReplayIdentityWatch(t, dispatcher, replayIdentityRequest(1, 1, agentID, rows[0].Seq), writer)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
	for _, id := range []uint64{2, 3} {
		reviseReplayIdentityWatch(t, base, replayIdentityRequest(id, 1, agentID, rows[0].Seq))
		waitReplayIdentityAck(t, base, id)
	}
	assert.Equal(t, 1, countCatchUpCompletes(base))
	assertReplayIdentityFrames(t, base, agentID, 1, rows[1:])
}

func TestWatchReplayIdentityUsesEachAgentsIdentityUnderOneInterestUpdate(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	firstRows := createReplayIdentityRows(t, svc, "first-replay-agent")
	secondRows := createReplayIdentityRows(t, svc, "second-replay-agent")
	first := replayIdentityRequest(7, 9007199254740993, "first-replay-agent", firstRows[0].Seq)
	second := replayIdentityRequest(7, ^uint64(0), "second-replay-agent", secondRows[1].Seq)
	first.Agents = append(first.Agents, second.Agents...)
	writer := &replayIdentityWriter{testResponseWriter: base}
	openReplayIdentityWatch(t, dispatcher, first, writer)
	ack := waitReplayIdentityAck(t, base, 7)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 2 }, 30*time.Second, time.Millisecond)
	assertReplayIdentityFrames(t, base, "first-replay-agent", first.Agents[0].ReplayId, firstRows[1:])
	assertReplayIdentityFrames(t, base, "second-replay-agent", second.Agents[0].ReplayId, secondRows[2:])
	require.Len(t, ack.AgentStates, 2)
	assert.Equal(t, "first-replay-agent", ack.AgentStates[0].AgentId)
	assert.Equal(t, first.Agents[0].ReplayId, ack.AgentStates[0].ReplayId)
	assert.Equal(t, "second-replay-agent", ack.AgentStates[1].AgentId)
	assert.Equal(t, second.Agents[0].ReplayId, ack.AgentStates[1].ReplayId)
}

func TestWatchReplayIdentityReportsNoRegistrationForASkippedFailedOpening(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	rows := createReplayIdentityRows(t, svc, "failed-opening-agent")
	store := &replayIdentityLookupStore{DBTX: svc.DB}
	store.fail.Store(true)
	svc.Queries = db.New(store)
	first := replayIdentityRequest(1, 1, "failed-opening-agent", rows[0].Seq)
	second := proto.Clone(first).(*leapmuxv1.WatchEventsRequest)
	second.UpdateId = 2
	payload, err := proto.Marshal(second)
	require.NoError(t, err)
	writer := &replayIdentityWriter{testResponseWriter: base, onBind: func(controller channel.StreamController) { controller.OnClientFrame(payload) }}
	openReplayIdentityWatch(t, dispatcher, first, writer)
	ack := waitReplayIdentityAck(t, base, 2)
	require.Len(t, ack.RejectedAgents, 1)
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED, ack.RejectedAgents[0].Reason)
	assert.Empty(t, ack.AgentStates)
	assert.Empty(t, svc.Watchers.AgentModesForChannel(base.ChannelID()))
	assert.Zero(t, countCatchUpCompletes(base))
	store.fail.Store(false)
	reviseReplayIdentityWatch(t, base, replayIdentityRequest(3, 3, "failed-opening-agent", rows[0].Seq))
	ack = waitReplayIdentityAck(t, base, 3)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
	require.Len(t, ack.AgentStates, 1)
	assert.Equal(t, uint64(3), ack.AgentStates[0].ReplayId)
	assertReplayIdentityFrames(t, base, "failed-opening-agent", 3, rows[1:])
}

func TestWatchReplayIdentityStartsANewLifetimeAfterSkippedDemotion(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	const agentID = "skipped-demotion-agent"
	rows := createReplayIdentityRows(t, svc, agentID)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseAck := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAck()
	writer := &replayIdentityWriter{testResponseWriter: base, onSend: func(message *leapmuxv1.InnerStreamMessage) {
		var response leapmuxv1.WatchEventsResponse
		if proto.Unmarshal(message.Payload, &response) == nil && response.GetUpdateAck().GetUpdateId() == 2 {
			close(entered)
			<-release
		}
	}}
	openReplayIdentityWatch(t, dispatcher, replayIdentityRequest(1, 1, agentID, rows[0].Seq), writer)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
	reviseReplayIdentityWatch(t, base, replayIdentityRequest(2, 1, agentID, rows[0].Seq))
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the second ACK did not reach its held sender")
	}
	reviseReplayIdentityWatch(t, base, &leapmuxv1.WatchEventsRequest{UpdateId: 3, Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY}}})
	reviseReplayIdentityWatch(t, base, replayIdentityRequest(4, 4, agentID, rows[1].Seq))
	releaseAck()
	ack := waitReplayIdentityAck(t, base, 4)
	reviseReplayIdentityWatch(t, base, replayIdentityRequest(5, 4, agentID, rows[1].Seq))
	waitReplayIdentityAck(t, base, 5)
	assert.Equal(t, 2, countCatchUpCompletes(base))
	var ids []string
	for _, event := range decodeAgentEvents(base) {
		if event.Replay && event.ReplayId == 4 {
			if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
				ids = append(ids, message.Id)
			}
		}
	}
	assert.Equal(t, []string{rows[2].ID}, ids)
	require.Len(t, ack.AgentStates, 1)
	assert.Equal(t, uint64(4), ack.AgentStates[0].ReplayId)
}

type replayIdentityLookupStore struct {
	db.DBTX
	fail atomic.Bool
}

func (store *replayIdentityLookupStore) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if store.fail.Load() && strings.HasPrefix(query, "-- name: ListAgentsByIDs ") {
		return nil, errors.New("the agent lookup failed")
	}
	return store.DBTX.QueryContext(ctx, query, args...)
}

func TestWatchReplayIdentityLookupFailureReportsRegisteredLifetime(t *testing.T) {
	t.Parallel()
	svc, dispatcher, base := setupTestService(t)
	const agentID = "lookup-lifetime-agent"
	rows := createReplayIdentityRows(t, svc, agentID)
	store := &replayIdentityLookupStore{DBTX: svc.DB}
	svc.Queries = db.New(store)
	writer := &replayIdentityWriter{testResponseWriter: base}
	openReplayIdentityWatch(t, dispatcher, replayIdentityRequest(1, 1, agentID, rows[0].Seq), writer)
	require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
	store.fail.Store(true)
	reviseReplayIdentityWatch(t, base, replayIdentityRequest(2, 1, agentID, rows[0].Seq))
	ack := waitReplayIdentityAck(t, base, 2)
	require.Len(t, ack.RejectedAgents, 1)
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED, ack.RejectedAgents[0].Reason)
	require.Len(t, ack.AgentStates, 1)
	assert.Equal(t, uint64(1), ack.AgentStates[0].ReplayId)
	assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, svc.Watchers.AgentModesForChannel(base.ChannelID())[agentID])
	assert.Equal(t, 1, countCatchUpCompletes(base))
}

func TestWatchReplayIdentityRejectsChangedRegisteredTuple(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"replay mode", "cursor", "window tail value", "absent versus zero", "zero versus absent"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, base := setupTestService(t)
			const agentID = "tuple-lifetime-agent"
			rows := createReplayIdentityRows(t, svc, agentID)
			first := replayIdentityRequest(1, 1, agentID, rows[0].Seq)
			if field == "absent versus zero" {
				first.Agents[0].WindowTailSeq = nil
			}
			if field == "zero versus absent" {
				*first.Agents[0].WindowTailSeq = 0
			}
			writer := &replayIdentityWriter{testResponseWriter: base}
			openReplayIdentityWatch(t, dispatcher, first, writer)
			require.Eventually(t, func() bool { return countCatchUpCompletes(base) == 1 }, 30*time.Second, time.Millisecond)
			next := proto.Clone(first).(*leapmuxv1.WatchEventsRequest)
			next.UpdateId = 2
			switch field {
			case "replay mode":
				next.Agents[0].Replay = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST
			case "cursor":
				next.Agents[0].CursorSeq++
			case "window tail value":
				*next.Agents[0].WindowTailSeq = 0
			case "absent versus zero":
				zero := int64(0)
				next.Agents[0].WindowTailSeq = &zero
			case "zero versus absent":
				next.Agents[0].WindowTailSeq = nil
			}
			reviseReplayIdentityWatch(t, base, next)
			require.Eventually(t, func() bool {
				return streamEndedWithError(base) || (lastWatchUpdateAck(t, base) != nil && lastWatchUpdateAck(t, base).UpdateId == 2)
			}, 30*time.Second, time.Millisecond)
			assert.True(t, streamEndedWithError(base))
			if streamEndedWithError(base) {
				for _, stream := range base.streamsSnapshot() {
					if stream.IsError {
						assert.Equal(t, int32(codes.InvalidArgument), stream.ErrorCode)
					}
				}
			}
		})
	}
}

func TestWatchReplayIdentityRequiresPositiveFullAndSupportsLargestValue(t *testing.T) {
	t.Parallel()
	for _, identity := range []uint64{0, ^uint64(0)} {
		t.Run(func() string {
			if identity == 0 {
				return "zero"
			}
			return "largest"
		}(), func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, base := setupTestService(t)
			const agentID = "integer-lifetime-agent"
			rows := createReplayIdentityRows(t, svc, agentID)
			writer := &replayIdentityWriter{testResponseWriter: base}
			openReplayIdentityWatch(t, dispatcher, replayIdentityRequest(1, identity, agentID, rows[0].Seq), writer)
			require.Eventually(t, func() bool { return streamEndedWithError(base) || countCatchUpCompletes(base) > 0 }, 30*time.Second, time.Millisecond)
			if identity == 0 {
				assert.True(t, streamEndedWithError(base))
				assert.Zero(t, countCatchUpCompletes(base))
			} else {
				assertReplayIdentityFrames(t, base, agentID, identity, rows[1:])
			}
		})
	}
}
