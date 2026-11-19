package cmd

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/validate"
)

// renameDispatcher answers the ONE inner RPC each arm of `tab rename`
// makes, with a stored title that DIFFERS from the requested one.
//
// The difference is the whole point. The worker cleans a title with the rule
// that validate.CleanName documents, and it never refuses one, so the name the
// tab carries afterwards is the REPLY's title. A stub that echoes the request
// cannot tell the two sources apart, and a leaf that prints the request passes
// against it.
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

func (d *renameDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	var reply proto.Message
	var title string
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure:
		var in leapmuxv1.RenameAgentRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		title = in.GetTitle()
		reply = &leapmuxv1.RenameAgentResponse{Title: d.stored}
	case leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure:
		var in leapmuxv1.RenameTerminalRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		title = in.GetTitle()
		reply = &leapmuxv1.RenameTerminalResponse{Title: d.stored}
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
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
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

// renameTabThrough drives the whole `tab rename` leaf against a worker that
// stores `stored` for whatever title it receives, and returns the data
// half of the envelope the leaf printed.
//
// tab declares what the hub's LocateTab answers with, which is what
// selects the branch: the agent branch dispatches RenameAgent and the
// terminal branch dispatches RenameTerminal.
func renameTabThrough(t *testing.T, tab *leapmuxv1.WorkspaceTab, stored, title string) (map[string]any, *renameDispatcher) {
	t.Helper()
	disp := &renameDispatcher{stored: stored}
	startSpawnIPC(t, &recordingHub{locateTab: tab}, disp)

	return runControl(t, "tab", "rename", tab.GetTab().GetTabId(), title).Data(t), disp
}

// agentTab and terminalTab are the two tabs LocateTab answers with. Both
// live on the worker the spawn harness stands up (worker-A), so the leaf
// derives the worker from the tab id alone -- the state every invocation
// from inside a worker-spawned agent starts in.
func agentTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"},
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
}

func terminalTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-2"},
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
}

// The agent arm must print the title the WORKER stored, not the one the
// operator typed. The worker cleans a title with the rule that
// validate.CleanName documents, so echoing the title back reports a name no tab
// carries -- and the operator's next command, which addresses the tab by that
// name, finds nothing.
//
// The requested title carries a TAB, which the rule folds to one space, and a
// `$` and a `%`, which the rule keeps. The pair is deliberate: the stored
// title must differ from the requested one for this test to bite, and the
// fixture has to state a difference the CURRENT rule can produce. It read
// ("release 100", `release $100%`) before, which the relaxed rule can no
// longer produce at all -- the worker now stores `release $100%` unchanged, so
// the stub was reporting a transformation that exists nowhere.
func TestRunTabRename_AgentPrintsTheWorkersStoredTitle(t *testing.T) {
	data, disp := renameTabThrough(t, agentTab(), `release $100%`, "release\t$100%")

	assert.Equal(t, `release $100%`, data["title"],
		"the envelope must carry the worker's cleaned title, not the requested one")
	assert.Equal(t, "agent-2", data["id"])
	assert.Equal(t, "agent", data["type"])

	assert.Equal(t, []string{leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure}, disp.called(),
		"the agent arm dispatches RenameAgent and nothing else")
	assert.Equal(t, []string{"release\t$100%"}, disp.sentTitles(),
		"the CLI sends the operator's text unchanged; the worker owns the cleaning rule")
}

// The terminal arm holds the same contract over its own RPC. The two arms
// print from separate statements, so one of them can regress alone.
func TestRunTabRename_TerminalPrintsTheWorkersStoredTitle(t *testing.T) {
	data, disp := renameTabThrough(t, terminalTab(), "build logs", "build\tlogs")

	assert.Equal(t, "build logs", data["title"],
		"the envelope must carry the worker's cleaned title, not the requested one")
	assert.Equal(t, "term-2", data["id"])
	assert.Equal(t, "terminal", data["type"])

	assert.Equal(t, []string{leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure}, disp.called(),
		"the terminal branch dispatches RenameTerminal and nothing else")
	assert.Equal(t, []string{"build\tlogs"}, disp.sentTitles())
}

// The case that hurts most: a title that cleans to NOTHING changes no
// row, and the worker answers with the name the tab already had. An
// envelope that echoed the request would report a rename that never
// happened, under a name that exists nowhere.
//
// The requested title is a run of invisible format characters. It read `$$$`
// before, which the relaxed rule now keeps whole, so the case no longer showed
// what its own name says -- silently, because the stub's `stored` field
// carries the answer either way.
func TestRunTabRename_ReportsTheUnchangedTitleWhenTheRequestCleansToNothing(t *testing.T) {
	const emptyingTitle = "\u200b\u00ad\ufeff"
	require.Empty(t, validate.CleanName(emptyingTitle),
		"this case only tests the fallback branch while the rule empties the requested title")

	data, disp := renameTabThrough(t, agentTab(), "nightly build", emptyingTitle)

	assert.Equal(t, "nightly build", data["title"],
		"a request that cleans to nothing leaves the old title, and the envelope must say so")
	assert.Equal(t, []string{emptyingTitle}, disp.sentTitles())
}

// A tab type that neither arm renames is refused as a usage error, and the
// refusal must reach the worker with nothing. `tab rename` binds no
// FixedTabType, so
// LocateTab can answer with any type the tab namespace holds -- a file tab
// today, another kind later -- and the default arm is the only thing that
// keeps such a tab from falling through to a silent success.
func TestRunTabRename_RefusesATabTypeNeitherArmRenames(t *testing.T) {
	disp := &renameDispatcher{stored: "unreachable"}
	startSpawnIPC(t, &recordingHub{locateTab: &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-2"},
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}}, disp)

	f := runControl(t, "tab", "rename", "file-2", "notes").requireFailure(t, "usage")
	assert.Contains(t, f.Message, "a file tab has no title")
	assert.Empty(t, disp.called(), "a tab neither arm renames must reach the worker with nothing")
}

func TestControlRejectsExplicitBlankTargetsBeforeCalling(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "tab", args: []string{"tab", "rename", "", "Changed"}},
		{name: "tile", args: []string{"tile", "split", ""}},
		{name: "workspace", args: []string{"workspace", "rename", "", "Changed"}},
		{name: "read", args: []string{"tab", "get", ""}},
		{name: "whitespace", args: []string{"tab", "move", " \t", "--tile", "other"}},
		{name: "variadic", args: []string{"agent", "wait", fullTestID("first"), ""}},
		{name: "placement", args: []string{"terminal", "open", "--workspace="}},
		{name: "hub empty", args: []string{"tab", "rename", "self", "Changed", "--hub="}},
		{name: "hub whitespace", args: []string{"tab", "get", "--hub", " \t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub, disp := startSelfRenameIPC(t)
			out := runControl(t, tc.args...)
			assert.Empty(t, disp.called(), "the command must not reach the Worker")
			assert.Empty(t, hub.called(), "the command must not reach the Hub")
			failure := out.requireFailure(t, cli.CodeUsage)
			assert.Contains(t, failure.Message, "empty")
		})
	}
}

func TestControlDefaultsOmittedReadTargetAndAcceptsExplicitSelf(t *testing.T) {
	_, disp := startSelfRenameIPC(t)

	assert.Equal(t, "agent-1", runControl(t, "tab", "get").Data(t)["id"])
	assert.Equal(t, "agent-1", runControl(t, "tab", "rename", "self", "Changed").Data(t)["id"])
	assert.Equal(t, []string{"Changed"}, disp.sentTitles())
}

func startSelfRenameIPC(t *testing.T) (*recordingHub, *renameDispatcher) {
	t.Helper()
	tab := agentTab()
	tab.Tab.TabId = "agent-1"
	hub := &recordingHub{locateTab: tab}
	disp := &renameDispatcher{stored: "Changed"}
	startSpawnIPC(t, hub, disp)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", tab.GetTab().GetTabId())
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "agent")
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", tab.GetWorkerId())
	return hub, disp
}
