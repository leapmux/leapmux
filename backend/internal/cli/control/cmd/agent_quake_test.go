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
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// quakeDispatcher answers the worker-local RPCs the `agent quake` leaves make
// and records what they carried, so a test can prove the right action reached
// the worker rather than that the leaf merely exited zero.
//
// `companionOwner`, when set, is the owner ListTerminals reports for the
// terminal id it is asked about -- which is how the leaf resolves the panel it
// is being run INSIDE.
type quakeDispatcher struct {
	companionOwner string

	mu      sync.Mutex
	methods []string
	actions []leapmuxv1.QuakePanelAction
	agents  []string
}

func (d *quakeDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()

	var reply proto.Message
	switch req.GetMethod() {
	case "SetQuakePanel":
		var in leapmuxv1.SetQuakePanelRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.actions = append(d.actions, in.GetAction())
		d.agents = append(d.agents, in.GetAgentId())
		d.mu.Unlock()
		reply = &leapmuxv1.SetQuakePanelResponse{}
	case "ListTerminals":
		var in leapmuxv1.ListTerminalsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		resp := &leapmuxv1.ListTerminalsResponse{}
		for _, id := range in.GetTabIds() {
			resp.Terminals = append(resp.Terminals, &leapmuxv1.TerminalInfo{
				TerminalId:   id,
				OwnerAgentId: d.companionOwner,
			})
		}
		reply = resp
	default:
		_ = w.SendError(int32(codes.Unimplemented), req.GetMethod())
		return
	}
	payload, err := proto.Marshal(reply)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload})
}

func (d *quakeDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

func quakeAgentTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:       "agent-1",
		WorkspaceId: "ws-1",
		WorkerId:    "worker-A",
	}
}

func quakeTerminalTab() *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		TabId:       "quake-1",
		WorkspaceId: "ws-1",
		WorkerId:    "worker-A",
	}
}

// runQuake drives one leaf and returns the data half of its envelope.
func runQuake(
	t *testing.T,
	run func(any, []string) error,
	tab *leapmuxv1.WorkspaceTab,
	disp *quakeDispatcher,
	args ...string,
) (map[string]any, *quakeDispatcher) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{locateTab: tab}, disp)

	out := withCapturedStdout(t, func() {
		require.NoError(t, run(fakeCmdCtx{}, append([]string{"--tab-id", tab.GetTabId()}, args...)))
	})

	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	require.Nil(t, env.Error, "the leaf must succeed: %s", out)
	return env.Data, disp
}

func TestRunAgentQuake_SendsEachActionToTheWorker(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		run    func(any, []string) error
		action leapmuxv1.QuakePanelAction
		word   string
	}{
		{"open", RunAgentQuakeOpen, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN, "open"},
		{"close", RunAgentQuakeClose, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE, "close"},
		{"toggle", RunAgentQuakeToggle, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE, "toggle"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			disp := &quakeDispatcher{}
			data, disp := runQuake(t, testCase.run, quakeAgentTab(), disp)

			assert.Equal(t, []string{"SetQuakePanel"}, disp.called())
			assert.Equal(t, []leapmuxv1.QuakePanelAction{testCase.action}, disp.actions)
			assert.Equal(t, []string{"agent-1"}, disp.agents)
			// The envelope restates the verb, so a caller reading the JSON
			// never has to map an enum number back to a word.
			assert.Equal(t, "agent-1", data["agent_id"])
			assert.Equal(t, testCase.word, data["action"])
		})
	}
}

// Run inside the Quake terminal itself, the ambient tab is that TERMINAL. The
// leaf resolves it to its owner rather than refusing, which is what makes
// "close the panel I am typing in" work -- the most natural use there is.
func TestRunAgentQuake_ResolvesACompanionTerminalToItsOwner(t *testing.T) {
	disp := &quakeDispatcher{companionOwner: "agent-1"}
	data, disp := runQuake(t, RunAgentQuakeClose, quakeTerminalTab(), disp)

	assert.Equal(t, []string{"ListTerminals", "SetQuakePanel"}, disp.called())
	assert.Equal(t, []string{"agent-1"}, disp.agents, "the command must name the OWNER")
	assert.Equal(t, "agent-1", data["agent_id"])
}

// A terminal that is a tab of its own owns no panel. Refused by name, because
// "nothing happened" and "you aimed at an ordinary terminal" need different
// fixes from the user.
func TestRunAgentQuake_RefusesATerminalThatIsNotACompanion(t *testing.T) {
	disp := &quakeDispatcher{companionOwner: ""}
	startSpawnIPC(t, &recordingHub{locateTab: quakeTerminalTab()}, disp)

	out := withCapturedStdout(t, func() {
		err := RunAgentQuakeToggle(fakeCmdCtx{}, []string{"--tab-id", "quake-1"})
		require.Error(t, err)
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "agent")
	assert.NotContains(t, disp.called(), "SetQuakePanel")
}

func TestRunAgentQuake_RequiresAnEntityID(t *testing.T) {
	clearRemoteEnv(t)
	out := withCapturedStdout(t, func() {
		err := RunAgentQuakeToggle(fakeCmdCtx{}, []string{"--hub", "https://stub"})
		require.Error(t, err)
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "tab-id")
}
