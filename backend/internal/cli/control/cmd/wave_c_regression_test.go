package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestResolveAdminIDPrefix_SupportsNameTitleAndBareFallback(t *testing.T) {
	idA := fullTestID("app-a")
	idB := fullTestID("app-b")
	idC := fullTestID("app-c")
	candidates := []cli.Candidate{
		{ID: idA, Kind: "app", Title: "Deploy Bot"},
		{ID: idB, Kind: "app", Title: "Duplicate"},
		{ID: idC, Kind: "app", Title: "duplicate"},
	}
	listCalls := 0
	list := func(_ context.Context) ([]cli.Candidate, error) {
		listCalls++
		return candidates, nil
	}

	// Full ID skips listing entirely.
	got, err := resolveAdminIDPrefix(context.Background(), idA, "app", list)
	require.NoError(t, err)
	assert.Equal(t, idA, got)
	assert.Equal(t, 0, listCalls)

	// Explicit id: bypasses listing; empty id: fails usage.
	got, err = resolveAdminIDPrefix(context.Background(), "id:literal-1", "app", list)
	require.NoError(t, err)
	assert.Equal(t, "literal-1", got)
	assert.Equal(t, 0, listCalls)
	_, err = resolveAdminIDPrefix(context.Background(), "id:  ", "app", list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	// name: and title: match case-insensitively.
	got, err = resolveAdminIDPrefix(context.Background(), "name:deploy bot", "app", list)
	require.NoError(t, err)
	assert.Equal(t, idA, got)

	got, err = resolveAdminIDPrefix(context.Background(), "title:Deploy Bot", "app", list)
	require.NoError(t, err)
	assert.Equal(t, idA, got)

	// Empty name:/title: fails usage; missing name: fails not_found.
	_, err = resolveAdminIDPrefix(context.Background(), "name:", "app", list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	_, err = resolveAdminIDPrefix(context.Background(), "name:missing", "app", list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	// Bare case-insensitive title fallback works when unique and fails ambiguous when duplicated.
	got, err = resolveAdminIDPrefix(context.Background(), "deploy bot", "app", list)
	require.NoError(t, err)
	assert.Equal(t, idA, got)

	_, err = resolveAdminIDPrefix(context.Background(), "duplicate", "app", list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeAmbiguousTarget, cli.FromError(err).Code)
	assert.Len(t, cli.FromError(err).Candidates, 2)

	// A 48-character title with spaces does not match resolve.IsGeneratedID, so it resolves via listing.
	const title48 = "Deploy Bot With Spaces Exactly 48 Chars Long Now"
	require.Len(t, title48, 48)
	candidates = append(candidates, cli.Candidate{ID: fullTestID("app-long"), Kind: "app", Title: title48})
	callsBefore := listCalls
	got, err = resolveAdminIDPrefix(context.Background(), title48, "app", list)
	require.NoError(t, err)
	assert.Equal(t, fullTestID("app-long"), got)
	assert.Equal(t, callsBefore+1, listCalls)

	// A valid 36-character UUID skips listing directly.
	const rawUUID = "87a19d5f-4838-4e34-9f0f-56130f0a62b1"
	callsBefore = listCalls
	got, err = resolveAdminIDPrefix(context.Background(), rawUUID, "app", list)
	require.NoError(t, err)
	assert.Equal(t, rawUUID, got)
	assert.Equal(t, callsBefore, listCalls)
}

func TestAgentAnswerOverIPC_OmittedRequestAndGlobalHandleWithText(t *testing.T) {
	agentID := fullTestID("ag-ans")
	req := pendingRequest("claim-unique-1", questionPrompt(&leapmuxv1.ControlQuestion{
		Header:   "Cache",
		Question: "Which cache backend?",
	}))

	t.Run("omitted request treats positional text as answer", func(t *testing.T) {
		w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{agentID: {req}}}
		startAgentWorker(t, agentID, w)

		data := runControl(t, "agent", "answer", agentID, "Use Redis").Data(t)
		assert.Equal(t, "claim-unique-1", data["id"])
		answers := w.recordedAnswers()
		require.Len(t, answers, 1)
		qAns := answers[0].GetDecision().GetQuestion().GetAnswers()
		require.Len(t, qAns, 1)
		assert.Equal(t, "Use Redis", qAns[0].GetCustomText())
	})

	t.Run("global request handle shifts second positional as answer text", func(t *testing.T) {
		w := &controlWorker{requests: map[string][]*leapmuxv1.AgentControlRequest{agentID: {req}}}
		startAgentWorker(t, agentID, w)

		data := runControl(t, "agent", "answer", "claim-unique-1", "Use Memcached").Data(t)
		assert.Equal(t, "claim-unique-1", data["id"])
		answers := w.recordedAnswers()
		require.Len(t, answers, 1)
		qAns := answers[0].GetDecision().GetQuestion().GetAnswers()
		require.Len(t, qAns, 1)
		assert.Equal(t, "Use Memcached", qAns[0].GetCustomText())
	})
}

func TestTileResizeAndSwapOverIPC(t *testing.T) {
	rootID := fullTestID("root-split")
	tileA := fullTestID("tile-a")
	tileB := fullTestID("tile-b")
	b := newStateBuilder().
		workspace("ws-1", rootID).
		splitNode(rootID, "", "a0").
		leafNode(tileA, rootID, "a0").
		leafNode(tileB, rootID, "a1")

	var submittedCount int
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: b.st,
		onSubmitOps: func(_ context.Context) ([]byte, error) {
			submittedCount++
			return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}})
		},
	}
	startSpawnIPC(t, hub, nil)

	// tile resize adjusts tileA to 0.7 and scales tileB to 0.3.
	resized := runControl(t, "tile", "resize", tileA, "--ratio", "0.7").Data(t)
	assert.Equal(t, rootID, resized["split_tile_id"])
	ratios := resized["ratios"].([]any)
	require.Len(t, ratios, 2)
	assert.InDelta(t, 0.7, ratios[0], 1e-9)
	assert.InDelta(t, 0.3, ratios[1], 1e-9)
	assert.Equal(t, 1, submittedCount)

	// tile resize refuses the workspace root tile.
	rootErr := runControl(t, "tile", "resize", rootID, "--ratio", "0.5").Failure(t)
	assert.Equal(t, cli.CodeConflict, rootErr.Code)

	// tile swap succeeds for two sibling tiles and refuses swapping a tile with itself.
	swapped := runControl(t, "tile", "swap", tileA, tileB).Data(t)
	assert.Equal(t, tileA, swapped["tile_a"])
	assert.Equal(t, tileB, swapped["tile_b"])
	assert.Equal(t, 2, submittedCount)

	selfErr := runControl(t, "tile", "swap", tileA, tileA).Failure(t)
	assert.Equal(t, cli.CodeUsage, selfErr.Code)
}

type dirtyWorktreeWorker struct {
	tabID       string
	uncommitted bool
}

func (w *dirtyWorktreeWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{
			Agents: []*leapmuxv1.AgentInfo{
				{
					Id:            w.tabID,
					ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
				},
			},
		})
	case leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure:
		writeProtoResponse(rw, &leapmuxv1.InspectLastTabCloseResponse{
			ShouldPrompt: true,
			BranchName:   "feature/wip",
			WorktreePath: "/repo/wt-wip",
			GitState: &leapmuxv1.BranchGitState{
				HasUncommittedChanges: w.uncommitted,
				UnpushedCommitCount:   2,
			},
		})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestWorkspaceDeleteDirtyWorktreeGuard(t *testing.T) {
	rootID := fullTestID("root-ws")
	tabID := fullTestID("ag-dirty")
	b := newStateBuilder().
		workspace("ws-1", rootID).
		leafNode(rootID, "", "a0").
		tab(tabID, rootID, leapmuxv1.TabType_TAB_TYPE_AGENT)
	b.st.Tabs[tabID].WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}

	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: b.st,
	}
	startSpawnIPC(t, hub, &dirtyWorktreeWorker{tabID: tabID, uncommitted: true})

	// Without --allow-busy, guardWorkspaceDirtyWorktrees refuses before calling DeleteWorkspace.
	o := runControl(t, "workspace", "delete", "ws-1")
	assert.Equal(t, cli.ExitRefused, o.Code)
	e := o.Failure(t)
	assert.Equal(t, cli.CodeTabBusyRefused, e.Code)
	assert.Contains(t, e.Message, "feature/wip")
	assert.Contains(t, e.Message, "uncommitted changes")
	assert.Contains(t, e.Message, "2 unpushed commits")
}
