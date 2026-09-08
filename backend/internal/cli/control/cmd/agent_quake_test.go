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
type quakeDispatcher struct {
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

// "Close the panel in front of me", which is the most natural use of this
// command, and the one that used to fail.
//
// Inside the Quake terminal the worker exports the OWNER AGENT as the ambient
// tab -- a companion has no CRDT tab, so the hub answers NotFound for its own
// id and the whole resolve fails before the leaf runs (see TerminalSpawning in
// `internal/worker/controlipc`). This test is the CLI half of that contract: it
// exports exactly the pair a Quake shell carries, passes NO --tab-id, and
// requires that the agent's own panel is what the worker is asked to hide.
func TestRunAgentQuake_ClosesTheOwnerPanelFromInsideTheQuakeShell(t *testing.T) {
	disp := &quakeDispatcher{}
	startSpawnIPC(t, &recordingHub{locateTab: quakeAgentTab()}, disp)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-1")
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "agent")

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAgentQuakeClose(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  map[string]any    `json:"data"`
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	require.Nil(t, env.Error, "the leaf must succeed with no --tab-id: %s", out)
	assert.Equal(t, []string{"SetQuakePanel"}, disp.called())
	assert.Equal(t, []string{"agent-1"}, disp.agents)
	assert.Equal(t, "close", env.Data["action"])
}

// The env default is pinned to the AGENT spelling, so an ordinary TERMINAL tab
// supplies no id: `agent quake` acts on agent tabs, and a shell in a terminal
// tab of its own has no panel to hide. The user gets the "pass --tab-id" hint
// rather than a request against the wrong tab.
func TestRunAgentQuake_IgnoresATerminalAmbientTab(t *testing.T) {
	disp := &quakeDispatcher{}
	startSpawnIPC(t, &recordingHub{locateTab: quakeAgentTab()}, disp)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "terminal-9")
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "terminal")

	out := withCapturedStdout(t, func() {
		err := RunAgentQuakeToggle(fakeCmdCtx{}, nil)
		require.Error(t, err)
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "tab-id")
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
