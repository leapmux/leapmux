package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// renameDispatcher answers the ONE inner RPC each arm of `tab rename`
// makes, with a stored title that DIFFERS from the requested one.
//
// The difference is the whole point. The worker cleans a title (it cuts
// it to 128 UTF-8 bytes and strips the control characters and " \ $ %),
// and it never refuses one, so the name the tab carries afterwards is
// the REPLY's title. A stub that echoes the request cannot tell the two
// sources apart, and a leaf that prints the request passes against it.
type renameDispatcher struct {
	// stored is the title the worker reports it kept.
	stored string

	mu sync.Mutex
	// methods records every method the leaf asked for, so a test can
	// prove which arm ran and that nothing else travelled.
	methods []string
	// requested records the title each request carried, so a test can
	// prove the CLI sends the operator's text unchanged and leaves the
	// cleaning to the worker.
	requested []string
}

func (d *renameDispatcher) DispatchWith(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	var reply proto.Message
	var title string
	switch req.GetMethod() {
	case "RenameAgent":
		var in leapmuxv1.RenameAgentRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		title = in.GetTitle()
		reply = &leapmuxv1.RenameAgentResponse{Title: d.stored}
	case "UpdateTerminalTitle":
		var in leapmuxv1.UpdateTerminalTitleRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		title = in.GetTitle()
		reply = &leapmuxv1.UpdateTerminalTitleResponse{Title: d.stored}
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}

	d.mu.Lock()
	d.requested = append(d.requested, title)
	d.mu.Unlock()

	payload, err := proto.Marshal(reply)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload})
}

func (d *renameDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

func (d *renameDispatcher) sentTitles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requested...)
}

// runTabRename drives the whole `tab rename` leaf against a worker that
// stores `stored` for whatever title it receives, and returns the data
// half of the envelope the leaf printed.
//
// tab declares what the hub's LocateTab answers with, which is what
// selects the arm: the agent arm dispatches RenameAgent and the terminal
// arm dispatches UpdateTerminalTitle.
func runTabRename(t *testing.T, tab *leapmuxv1.WorkspaceTab, stored, title string) (map[string]any, *renameDispatcher) {
	t.Helper()
	disp := &renameDispatcher{stored: stored}
	startSpawnIPC(t, &recordingHub{locateTab: tab}, disp)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunTabRename(fakeCmdCtx{}, []string{
			"--tab-id", tab.GetTabId(),
			"--tab-type", resolve.TabTypeWireName(tab.GetTabType()),
			"--title", title,
		}))
	})

	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	require.Nil(t, env.Error, "tab rename must succeed: %s", out)
	return env.Data, disp
}

// agentTab and terminalTab are the two tabs LocateTab answers with. Both
// live on the worker the spawn harness stands up (worker-A), so the leaf
// derives the worker from the tab id alone -- the state every invocation
// from inside a worker-spawned agent starts in.
func agentTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:       "agent-2",
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
}

func terminalTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabId:       "term-2",
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
}

// The agent arm must print the title the WORKER stored, not the one the
// operator typed. The worker cuts a title to 128 UTF-8 bytes and strips
// the control characters and " \ $ %, so echoing --title back reports a
// name no tab carries -- and the operator's next command, which addresses
// the tab by that name, finds nothing.
func TestRunTabRename_AgentPrintsTheWorkersStoredTitle(t *testing.T) {
	data, disp := runTabRename(t, agentTab(), "release 100", `release $100%`)

	assert.Equal(t, "release 100", data["title"],
		"the envelope must carry the worker's cleaned title, not the requested one")
	assert.Equal(t, "agent-2", data["tab_id"])
	assert.Equal(t, "agent", data["tab_type"])

	assert.Equal(t, []string{"RenameAgent"}, disp.called(),
		"the agent arm dispatches RenameAgent and nothing else")
	assert.Equal(t, []string{`release $100%`}, disp.sentTitles(),
		"the CLI sends the operator's text unchanged; the worker owns the cleaning rule")
}

// The terminal arm holds the same contract over its own RPC. The two arms
// print from separate statements, so one of them can regress alone.
func TestRunTabRename_TerminalPrintsTheWorkersStoredTitle(t *testing.T) {
	data, disp := runTabRename(t, terminalTab(), "build logs", "build\tlogs")

	assert.Equal(t, "build logs", data["title"],
		"the envelope must carry the worker's cleaned title, not the requested one")
	assert.Equal(t, "term-2", data["tab_id"])
	assert.Equal(t, "terminal", data["tab_type"])

	assert.Equal(t, []string{"UpdateTerminalTitle"}, disp.called(),
		"the terminal arm dispatches UpdateTerminalTitle and nothing else")
	assert.Equal(t, []string{"build\tlogs"}, disp.sentTitles())
}

// The case that hurts most: a title that cleans to NOTHING changes no
// row, and the worker answers with the name the tab already had. An
// envelope that echoed the request would report a rename that never
// happened, under a name that exists nowhere.
func TestRunTabRename_ReportsTheUnchangedTitleWhenTheRequestCleansToNothing(t *testing.T) {
	data, disp := runTabRename(t, agentTab(), "nightly build", `$$$`)

	assert.Equal(t, "nightly build", data["title"],
		"a request that cleans to nothing leaves the old title, and the envelope must say so")
	assert.Equal(t, []string{`$$$`}, disp.sentTitles())
}

// A tab type that neither arm renames is refused, and the refusal must
// reach the worker with nothing. `tab rename` binds no FixedTabType, so
// LocateTab can answer with any type the tab namespace holds -- a file tab
// today, another kind later -- and the default arm is the only thing that
// keeps such a tab from falling through to a silent success.
func TestRunTabRename_RefusesATabTypeNeitherArmRenames(t *testing.T) {
	disp := &renameDispatcher{stored: "unreachable"}
	startSpawnIPC(t, &recordingHub{locateTab: &leapmuxv1.WorkspaceTab{
		TabType:     leapmuxv1.TabType_TAB_TYPE_FILE,
		TabId:       "file-2",
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}}, disp)

	out := withCapturedStdout(t, func() {
		// The handler returns the envelope it printed, so an error here is
		// the expected outcome.
		require.Error(t, RunTabRename(fakeCmdCtx{}, []string{
			"--tab-id", "file-2", "--tab-type", "file", "--title", "notes",
		}))
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	assert.Equal(t, "not_found", env.Error["code"])
	assert.Contains(t, env.Error["message"], "file-2")
	assert.Empty(t, disp.called(), "a tab neither arm renames must reach the worker with nothing")
}
