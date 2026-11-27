package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestContextUse_RejectsEmptyFlagsAndClearRemovesFields(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "")
	t.Setenv("LEAPMUX_HUB", "")

	// Save all four defaults first (including canonicalizing trailing slash on --hub).
	o := runControl(t, "context", "use",
		"--hub", "https://hub.example.com/",
		"--worker", "build-box",
		"--workspace", "Release 1.4",
		"--provider", "claude",
	)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)

	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example.com", cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker)
	assert.Equal(t, "Release 1.4", cfg.Workspace)
	assert.Equal(t, "claude", cfg.Provider)

	// Passing an empty or whitespace-only value to any `context use` flag fails
	// with CodeUsage and preserves the saved defaults.
	for _, flag := range []string{"--hub", "--worker", "--workspace", "--provider"} {
		for _, empty := range []string{"", "   "} {
			o = runControl(t, "context", "use", flag, empty)
			o.requireFailure(t, cli.CodeUsage)
		}
	}

	cfg, err = control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example.com", cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker)
	assert.Equal(t, "Release 1.4", cfg.Workspace)
	assert.Equal(t, "claude", cfg.Provider)

	// `context clear --field` clears each saved field individually.
	for _, field := range []string{"hub", "worker", "workspace", "provider"} {
		o = runControl(t, "context", "clear", "--field", field)
		require.Equal(t, cli.ExitOK, o.Code, "field=%s stderr: %s", field, o.Stderr)
	}

	cfg, err = control.LoadContext()
	require.NoError(t, err)
	assert.Empty(t, cfg.Hub)
	assert.Empty(t, cfg.Worker)
	assert.Empty(t, cfg.Workspace)
	assert.Empty(t, cfg.Provider)
}

func TestHubHost_SanitizesIPv6Colons(t *testing.T) {
	host, err := control.HubHost("https://[::1]:8080")
	require.NoError(t, err)
	assert.NotContains(t, host, ":", "credential filename key must be valid on Windows")
	assert.Equal(t, "https_--1_8080", host)
}

func TestExtractRateLimitRows_SupportsMapValues(t *testing.T) {
	raw := map[string]any{
		contracts.RateLimitUpdateFieldMode: "snapshot",
		contracts.RateLimitUpdateFieldValues: map[string]any{
			"weekly": map[string]any{
				contracts.RateLimitFieldLabel:       "Weekly",
				contracts.RateLimitFieldStatus:      "allowed",
				contracts.RateLimitFieldUtilization: 0.75,
			},
			"daily": map[string]any{
				contracts.RateLimitFieldLabel:       "Daily",
				contracts.RateLimitFieldStatus:      "allowed_warning",
				contracts.RateLimitFieldUtilization: 0.9,
			},
		},
	}
	rows := extractRateLimitRows(raw)
	assert.Equal(t, []string{
		"Daily allowed_warning 90%",
		"Weekly allowed 75%",
	}, rows)
}

func TestLookupByIDPrefix_RequiresMin3CharsForPrefixMatch(t *testing.T) {
	items := []string{"abc123", "xyz789", "k"}
	idOf := func(s string) string { return s }

	// 1-2 char prefixes must not match a longer ID.
	_, _, ok := lookupByIDPrefix(items, "a", idOf)
	assert.False(t, ok)
	_, _, ok = lookupByIDPrefix(items, "ab", idOf)
	assert.False(t, ok)

	// 3+ char unique prefix matches.
	got, _, ok := lookupByIDPrefix(items, "abc", idOf)
	require.True(t, ok)
	assert.Equal(t, "abc123", got)

	// Exact match works at any length.
	got, _, ok = lookupByIDPrefix(items, "k", idOf)
	require.True(t, ok)
	assert.Equal(t, "k", got)
}

func TestAgentMessageList_QuietLinesAlwaysPrintsMessageSeqs(t *testing.T) {
	list := agentMessageList{
		Messages: []agentMessage{{Seq: 10}, {Seq: 11}},
		Todos:    []agentTodoRow{{ID: "todo-1", Content: "ship"}},
	}
	assert.Equal(t, []string{"10", "11"}, list.QuietLines())
}

func TestTabClose_WorktreeDiscardFailureAndBatchRenderText(t *testing.T) {
	r := &tabCloseResult{
		ID:               "tab-1",
		Type:             "agent",
		Status:           "partial_failure",
		Worktree:         "discard",
		WorkerCloseError: "worker disconnected",
	}
	assert.True(t, r.worktreeDiscardFailed(), "RPC failure during --worktree discard must report failure")

	var buf bytes.Buffer
	batch := tabCloseBatchResult{Closed: []*tabCloseResult{r}}
	require.NoError(t, batch.RenderText(&buf))
	assert.Contains(t, buf.String(), "closed agent tab-1 [partial_failure]")
	assert.Contains(t, buf.String(), "warning: worker teardown error: worker disconnected")
}

func TestTerminalChunkSanitizer_HandlesSplitSequences(t *testing.T) {
	s := cli.NewTerminalSanitizer()
	// Split 8-bit C1 CSI and split DCS across two chunks.
	out1 := s.Write([]byte("hello\xc2\x9b31"))
	out2 := s.Write([]byte("mworld\x1bPpayload"))
	out3 := s.Write([]byte("more\x1b\\!"))
	rem := s.Flush()
	assert.Equal(t, "helloworld!", out1+out2+out3+rem)

	// Split long OSC (>256 bytes) across chunks.
	s2 := cli.NewTerminalSanitizer()
	longPayload := strings.Repeat("A", 300)
	a := s2.Write([]byte("pre\x1b]52;c;" + longPayload))
	b := s2.Write([]byte("end\x07post"))
	assert.Equal(t, "prepost", a+b+s2.Flush())
}

// siblingDirtyWorktreeWorker simulates InspectTabsClose evaluating two sibling
// tabs closing together on the same dirty worktree (no surviving sibling outside
// the closing set).
type siblingDirtyWorktreeWorker struct {
	tabIDs []string
	dir    string
}

func (w *siblingDirtyWorktreeWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var agents []*leapmuxv1.AgentInfo
		for _, id := range w.tabIDs {
			agents = append(agents, &leapmuxv1.AgentInfo{
				Id:            id,
				ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
			})
		}
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: agents})
	case leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure:
		items := make([]*leapmuxv1.TabCloseInspection, 0, len(w.tabIDs))
		for _, id := range w.tabIDs {
			items = append(items, &leapmuxv1.TabCloseInspection{
				Tab:          &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: id},
				ShouldPrompt: true,
				IsWorktree:   true,
				BranchName:   "feature/shared",
				WorkingDir:   w.dir,
				WorktreePath: w.dir,
				GitState: &leapmuxv1.BranchGitState{
					HasUncommittedChanges: true,
					UnpushedCommitCount:   1,
				},
			})
		}
		writeProtoResponse(rw, &leapmuxv1.InspectTabsCloseResponse{Items: items})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestWorkspaceDeleteDirtyWorktreeGuard_SiblingTabsFastPath(t *testing.T) {
	rootID := fullTestID("root-ws")
	tab1 := fullTestID("ag-sib-1")
	tab2 := fullTestID("ag-sib-2")
	b := newStateBuilder().
		workspace("ws-1", rootID).
		leafNode(rootID, "", "a0").
		tab(tab1, rootID, leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(tab2, rootID, leapmuxv1.TabType_TAB_TYPE_AGENT)
	b.st.Tabs[tab1].WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}
	b.st.Tabs[tab2].WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}

	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: b.st,
	}
	startSpawnIPC(t, hub, &siblingDirtyWorktreeWorker{
		tabIDs: []string{tab1, tab2},
		dir:    "/work/shared-wt",
	})

	// When 2 tabs share the same dirty worktree in the workspace being deleted,
	// InspectTabsClose evaluates them together and workspace delete refuses.
	o := runControl(t, "workspace", "delete", "ws-1")
	assert.Equal(t, cli.ExitRefused, o.Code)
	e := o.Failure(t)
	assert.Equal(t, cli.CodeTabBusyRefused, e.Code)
	assert.Contains(t, e.Message, "feature/shared")
	assert.Contains(t, e.Message, "uncommitted changes")
	assert.Contains(t, e.Message, "1 unpushed commit")
}

func TestEventsAccountReplay_AllBacklogCaughtUp(t *testing.T) {
	k1 := workerHistoryKey{id: "w-1", tabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	k2 := workerHistoryKey{id: "w-2", tabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}
	r := &accountReplay{
		hubExpected: true,
		hubState:    replaySourceState{live: false},
		hub: &layoutPageConsumer{
			position:   &eventHistoryPosition{Sequence: 3},
			replayHigh: 5,
		},
		state: &eventCheckpoint{
			Version: eventCheckpointVersion,
			Scope:   accountCheckpointScope,
			Account: &accountEventCheckpoint{
				Workers: map[string]accountWorkerPositions{
					"w-1": {"agent": {Sequence: 10}},
					"w-2": {"terminal": {Sequence: 2}},
				},
			},
		},
		workers: map[workerHistoryKey]*accountWorkerWatch{
			k1: {key: k1, replaySourceState: replaySourceState{live: true, high: 10}},
		},
	}
	assert.False(t, r.allBacklogCaughtUp(), "pending hub backlog must not be marked caught up")

	r.hubState.live = true
	assert.False(t, r.allBacklogCaughtUp(), "hub sequence below replayHigh must keep snapshot running")

	r.hub.position.Sequence = 5
	assert.True(t, r.allBacklogCaughtUp(), "hub caught up + all workers caught up means backlog drained")

	r.workers[k2] = &accountWorkerWatch{key: k2, replaySourceState: replaySourceState{live: false, high: 4}}
	assert.False(t, r.allBacklogCaughtUp(), "worker not yet live must keep snapshot running")

	r.workers[k2].live = true
	assert.False(t, r.allBacklogCaughtUp(), "worker sequence below high must keep snapshot running")

	r.workers[k2].stopped = cli.Newf(cli.CodeWorkerOffline, "offline")
	assert.True(t, r.allBacklogCaughtUp(), "stopped worker counts as finished for snapshot drain")
}

func TestParseAdminUserTarget_AndNotFoundCheck(t *testing.T) {
	got, err := parseAdminUserTarget("id:Ab3dE9")
	require.NoError(t, err)
	assert.True(t, got.isSet())
	assert.Equal(t, "Ab3dE9", got.selector.GetId())
	assert.Equal(t, "Ab3dE9", got.prefix)

	got, err = parseAdminUserTarget("username:alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.selector.GetUsername())
	assert.Empty(t, got.prefix, "explicit username: must never trigger ID prefix scan")

	fullID := fullTestID("user-alpha")
	got, err = parseAdminUserTarget(fullID)
	require.NoError(t, err)
	assert.Equal(t, fullID, got.selector.GetId())
	assert.Empty(t, got.prefix, "full 48-char ID must be sent directly by ID")

	// A 48-character string with non-ID characters (dots/spaces) is not a generated ID.
	const nonID48 = "user.with.dots.and.exact.length.of.48.characters"
	require.Len(t, nonID48, 48)
	got, err = parseAdminUserTarget(nonID48)
	require.NoError(t, err)
	assert.Equal(t, nonID48, got.selector.GetUsername())
	assert.Equal(t, nonID48, got.prefix)

	const userUUID = "87a19d5f-4838-4e34-9f0f-56130f0a62b1"
	got, err = parseAdminUserTarget(userUUID)
	require.NoError(t, err)
	assert.Equal(t, userUUID, got.selector.GetId())
	assert.Empty(t, got.prefix)

	got, err = parseAdminUserTarget("id:" + fullID)
	require.NoError(t, err)
	assert.Equal(t, fullID, got.selector.GetId())
	assert.Empty(t, got.prefix)

	got, err = parseAdminUserTarget("id:ab")
	require.NoError(t, err)
	assert.Equal(t, "ab", got.selector.GetId())
	assert.Empty(t, got.prefix)

	assert.True(t, isAdminUserNotFound(connect.NewError(connect.CodeNotFound, "missing")))
	assert.True(t, isAdminUserNotFound(cli.Newf(cli.CodeNotFound, "missing")))
	assert.False(t, isAdminUserNotFound(connect.NewError(connect.CodePermissionDenied, "denied")))
}
