package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestControlCompletionCompletesScopeFlagValues(t *testing.T) {
	hub, _ := startSelfRenameIPC(t)
	out := runTree(t, NewCommand(), "", "__complete", "tab", "list", "--workspace", "ws")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.Contains(t, out.Stdout, "ws-1\tFirst\n")
	assert.NotContains(t, out.Stdout, "self\n")
	assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure}, hub.called())
}

func TestControlCompletionCompletesDeclaredFlagEnums(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"agent", "wait", "--until", "wait"}, want: "waiting_for_user\n"},
		{args: []string{"events", "--kind", "agent,terminal.n"}, want: "agent,terminal.notification\n"},
	} {
		out := runTree(t, NewCommand(), "", append([]string{"__complete"}, tc.args...)...)
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, tc.want)
		assert.NotContains(t, out.Stdout, "working\n")
	}
}

func TestControlCompletionDoesNotQueryTheTabSocketWithABlankHub(t *testing.T) {
	for _, value := range []string{"", " \t"} {
		hub, _ := startSelfRenameIPC(t)
		out := runTree(t, NewCommand(), "", "__complete", "tab", "get", "--hub", value, "agent")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.NotContains(t, out.Stdout, "agent-1")
		assert.Empty(t, hub.called())
	}
}

func TestControlNestedGroupHelpPointsToTheAgentGuide(t *testing.T) {
	for _, group := range [][]string{{"agent", "queue"}, {"terminal", "quake"}, {"admin", "worker", "reg-key"}} {
		out := runControlText(t, append(group, "--help")...)
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.True(t, strings.HasSuffix(out.Stdout, guideFooter+"\n"), "group %q", group)
	}
}

func TestControlCompletionCompletesLocalWorkerPins(t *testing.T) {
	isolateCLIEnv(t)
	const hub = "https://pins.example.invalid"
	pins, err := control.NewPinStore(hub)
	require.NoError(t, err)
	commit, err := pins.Resolve(t.Context(), "spare-worker", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
	require.NoError(t, err)
	require.NoError(t, commit(t.Context()))
	for _, verb := range []string{"get", "remove"} {
		out := runTree(t, NewCommand(), "", "__complete", "worker", "pins", verb, "--hub", hub, "spare")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "spare-worker\n")
		assert.NotContains(t, out.Stdout, "self\n")
	}

	host, err := control.HubHost(hub)
	require.NoError(t, err)
	path := filepath.Join(os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR"), host, "pins.json")
	require.NoError(t, os.WriteFile(path, []byte("{invalid"), 0o644))
	out := runTree(t, NewCommand(), "", "__complete", "worker", "pins", "get", "--hub", hub, "spare")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.NotContains(t, out.Stdout, "spare-worker")
}

func TestControlCompletionOmitsTheCallingAgentFromWaitTargets(t *testing.T) {
	startSelfRenameIPC(t)
	out := runTree(t, NewCommand(), "", "__complete", "agent", "wait", "")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.NotContains(t, out.Stdout, "agent-1")
	assert.NotContains(t, out.Stdout, "self\n")
}

type parentCompletionDispatcher struct{}

func (parentCompletionDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var listReq leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &listReq)
		var agents []*leapmuxv1.AgentInfo
		for _, id := range listReq.GetTabIds() {
			switch id {
			case "child-agent-1":
				agents = append(agents, &leapmuxv1.AgentInfo{Id: "child-agent-1", ParentAgentId: "parent-agent-1", WorkerId: "worker-A"})
			case "parent-agent-1":
				agents = append(agents, &leapmuxv1.AgentInfo{
					Id:       "parent-agent-1",
					WorkerId: "worker-A",
					OptionGroups: []*leapmuxv1.AvailableOptionGroup{{
						Id:      "model",
						Options: []*leapmuxv1.AvailableOption{{Id: "claude-opus", Name: "Claude Opus"}},
					}},
				})
			}
		}
		payload, _ := proto.Marshal(&leapmuxv1.ListAgentsResponse{Agents: agents})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	case leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure:
		var crReq leapmuxv1.ListControlRequestsRequest
		_ = proto.Unmarshal(req.GetPayload(), &crReq)
		var reqs []*leapmuxv1.AgentControlRequest
		if crReq.GetAgentId() == "parent-agent-1" {
			reqs = append(reqs, &leapmuxv1.AgentControlRequest{ClaimToken: "req-parent-1"})
		}
		payload, _ := proto.Marshal(&leapmuxv1.ListControlRequestsResponse{Requests: reqs})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		var qReq leapmuxv1.ListAgentInputQueueRequest
		_ = proto.Unmarshal(req.GetPayload(), &qReq)
		var items []*leapmuxv1.QueuedAgentInput
		if qReq.GetAgentId() == "parent-agent-1" {
			items = append(items, &leapmuxv1.QueuedAgentInput{Id: "inp-parent-1", Text: "follow up"})
		}
		payload, _ := proto.Marshal(&leapmuxv1.ListAgentInputQueueResponse{
			Snapshot: &leapmuxv1.AgentInputQueueSnapshot{Items: items},
		})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	default:
		_ = w.SendError(12, req.GetMethod())
	}
}

func TestControlCompletionResolvesParentTarget(t *testing.T) {
	startSpawnIPC(t, &recordingHub{}, parentCompletionDispatcher{})
	t.Setenv(controlenv.TabIDVariable, "child-agent-1")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	setOut := runTree(t, NewCommand(), "", "__complete", "agent", "set", "parent", "--model", "claude")
	require.Equal(t, cli.ExitOK, setOut.Code, setOut.Stderr)
	assert.Contains(t, setOut.Stdout, "claude-opus\tClaude Opus\n")

	answerOut := runTree(t, NewCommand(), "", "__complete", "agent", "answer", "parent", "req")
	require.Equal(t, cli.ExitOK, answerOut.Code, answerOut.Stderr)
	assert.Contains(t, answerOut.Stdout, "req-parent-1\t")

	queueOut := runTree(t, NewCommand(), "", "__complete", "agent", "queue", "move", "parent", "inp")
	require.Equal(t, cli.ExitOK, queueOut.Code, queueOut.Stderr)
	assert.Contains(t, queueOut.Stdout, "inp-parent-1\tfollow up\n")
}

func TestControlCompletionOffersChildrenForMultiAgentCommands(t *testing.T) {
	startSelfRenameIPC(t)

	for _, args := range [][]string{
		{"agent", "wait", ""},
		{"agent", "wait", "ch"},
		{"agent", "requests", ""},
		{"agent", "interrupt", ""},
		{"agent", "close", ""},
	} {
		out := runTree(t, NewCommand(), "", append([]string{"__complete"}, args...)...)
		require.Equal(t, cli.ExitOK, out.Code, "args=%v stderr=%s", args, out.Stderr)
		assert.Contains(t, out.Stdout, "children\n", "args=%v", args)
	}

	// Single-agent and non-agent commands do not offer "children".
	for _, args := range [][]string{
		{"agent", "get", ""},
		{"agent", "send", ""},
		{"agent", "approve", ""},
		{"tab", "close", ""},
		{"terminal", "close", ""},
	} {
		out := runTree(t, NewCommand(), "", append([]string{"__complete"}, args...)...)
		require.Equal(t, cli.ExitOK, out.Code, "args=%v stderr=%s", args, out.Stderr)
		assert.NotContains(t, out.Stdout, "children\n", "args=%v", args)
	}

	// Outside an agent tab, multi-agent commands do not offer "children".
	t.Setenv(controlenv.TabTypeVariable, "terminal")
	out := runTree(t, NewCommand(), "", "__complete", "agent", "wait", "")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.NotContains(t, out.Stdout, "children\n")

	// Inside an agent tab without a worker id, "children" is omitted.
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "")
	out = runTree(t, NewCommand(), "", "__complete", "agent", "wait", "")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.NotContains(t, out.Stdout, "children\n")
}
