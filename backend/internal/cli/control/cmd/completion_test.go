package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/optionids"
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

func TestCompletion_NilCommandContextDoesNotPanic(t *testing.T) {
	t.Run("completionContext handles nil command and nil context", func(t *testing.T) {
		ctx1, cancel1 := completionContext(nil)
		defer cancel1()
		_, hasDeadline1 := ctx1.Deadline()
		assert.True(t, hasDeadline1)

		ctx2, cancel2 := completionContext(&cobra.Command{})
		defer cancel2()
		_, hasDeadline2 := ctx2.Deadline()
		assert.True(t, hasDeadline2)
	})

	t.Run("adminCompletionClient handles nil command context", func(t *testing.T) {
		isolateCLIEnv(t)
		const hub = "https://admin-nilctx.example.invalid"
		require.NoError(t, control.SaveCredentials(hub, control.CredentialFile{
			HubURL:      hub,
			AccessToken: "tok",
			Username:    "alice",
		}))
		cmd := &cobra.Command{}
		cmd.Flags().String("hub", hub, "")
		require.NoError(t, cmd.Flags().Set("hub", hub))
		require.Nil(t, cmd.Context())

		client, ctx, cancel, ok := adminCompletionClient(cmd)
		require.True(t, ok)
		require.NotNil(t, client)
		require.NotNil(t, ctx)
		cancel()
	})

	t.Run("completeTargetsWithOptions handles nil command context", func(t *testing.T) {
		startSelfRenameIPC(t)
		cmd := &cobra.Command{}
		require.Nil(t, cmd.Context())

		cands, dir := completeTargetsWithOptions(cmd, resolve.KindWorkspace, "ws", targetCompletionOptions{})
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Contains(t, cands, "ws-1\tFirst")
	})

	t.Run("completePinnedWorkers handles nil command context", func(t *testing.T) {
		isolateCLIEnv(t)
		const hub = "https://pins-nilctx.example.invalid"
		pins, err := control.NewPinStore(hub)
		require.NoError(t, err)
		commit, err := pins.Resolve(t.Context(), "spare-worker", []byte("x25519"), []byte("mlkem"), []byte("slhdsa"))
		require.NoError(t, err)
		require.NoError(t, commit(t.Context()))

		cmd := &cobra.Command{}
		cmd.Flags().String("hub", hub, "")
		require.NoError(t, cmd.Flags().Set("hub", hub))
		require.Nil(t, cmd.Context())

		cands, dir := completePinnedWorkers(cmd, cli.ArgSpec{}, "spare")
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Equal(t, []string{"spare-worker"}, cands)
	})
}

func TestFormatCompletionCandidate_SanitizesValueAndDescription(t *testing.T) {
	assert.Equal(t, "value", formatCompletionCandidate("value", ""))
	assert.Equal(t, "value\tdesc", formatCompletionCandidate("value", "desc"))

	// Tabs, newlines, ANSI CSI/OSC escapes, and BiDi overrides are stripped or
	// collapsed in both value and description so only the single Cobra tab
	// separator remains.
	rawVal := "\x1b[31mval\twith\nbreak\u202e\x1b[0m"
	rawDesc := "line1\nline2\t\x1b]0;evil\x07ok\u2066"
	got := formatCompletionCandidate(rawVal, rawDesc)
	parts := strings.Split(got, "\t")
	require.Len(t, parts, 2, "candidate must contain exactly one tab separator: %q", got)
	assert.Equal(t, "val with break", parts[0])
	assert.Equal(t, "line1 line2 ok", parts[1])
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\n")

	// When the description sanitizes to empty, omit the trailing tab.
	assert.Equal(t, "clean", formatCompletionCandidate("clean", "\x1b[31m\x1b[0m"))
}

func TestControlCompletion_RootPersistentHubFlagCompletesOnChildCommands(t *testing.T) {
	isolateCLIEnv(t)
	require.NoError(t, control.SaveCredentials("https://hub-a.example.invalid", control.CredentialFile{
		HubURL:      "https://hub-a.example.invalid",
		AccessToken: "tok-a",
		Username:    "alice",
	}))
	require.NoError(t, control.SaveCredentials("https://hub-b.example.invalid", control.CredentialFile{
		HubURL:      "https://hub-b.example.invalid",
		AccessToken: "tok-b",
		Username:    "bob",
	}))

	for _, args := range [][]string{
		{"doctor", "--hub", "https://hub-"},
		{"worker", "list", "--hub", "https://hub-"},
		{"auth", "login", "--hub", "https://hub-"},
		{"context", "use", "--hub", "https://hub-"},
	} {
		out := runTree(t, NewCommand(), "", append([]string{"__complete"}, args...)...)
		require.Equal(t, cli.ExitOK, out.Code, "args=%v stderr=%s", args, out.Stderr)
		assert.Contains(t, out.Stdout, "https://hub-a.example.invalid\talice\n", "args=%v", args)
		assert.Contains(t, out.Stdout, "https://hub-b.example.invalid\tbob\n", "args=%v", args)
	}
}

type optionCompletionDispatcher struct {
	listAgentsCalls atomic.Int32
}

func (d *optionCompletionDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		d.listAgentsCalls.Add(1)
		var listReq leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &listReq)
		var agents []*leapmuxv1.AgentInfo
		for _, id := range listReq.GetTabIds() {
			agents = append(agents, &leapmuxv1.AgentInfo{
				Id:       id,
				WorkerId: "worker-A",
				OptionGroups: []*leapmuxv1.AvailableOptionGroup{
					{
						Id:      optionids.Model,
						Options: []*leapmuxv1.AvailableOption{{Id: "claude-opus", Name: "Claude Opus"}},
					},
					{
						Id:      optionids.Effort,
						Options: []*leapmuxv1.AvailableOption{{Id: "high", Name: "High"}},
					},
					{
						Id: optionids.PermissionMode,
						Options: []*leapmuxv1.AvailableOption{
							{Id: "acceptEdits", Name: "Accept Edits"},
							{Id: "plan", Name: "Plan"},
						},
					},
				},
			})
		}
		payload, _ := proto.Marshal(&leapmuxv1.ListAgentsResponse{Agents: agents})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	default:
		_ = w.SendError(12, req.GetMethod())
	}
}

func TestControlCompletion_PermissionModeMatchesOptionIDsPermissionMode(t *testing.T) {
	disp := &optionCompletionDispatcher{}
	startSpawnIPC(t, &recordingHub{}, disp)
	t.Setenv(controlenv.TabIDVariable, "agent-1")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	out := runTree(t, NewCommand(), "", "__complete", "agent", "set", "self", "--permission-mode", "acc")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.Contains(t, out.Stdout, "acceptEdits\tAccept Edits\n")
	assert.NotContains(t, out.Stdout, "plan\t")
}

func TestControlCompletion_AgentOpenDoesNotFallBackToSelfOptions(t *testing.T) {
	disp := &optionCompletionDispatcher{}
	startSpawnIPC(t, &recordingHub{}, disp)
	t.Setenv(controlenv.TabIDVariable, "agent-1")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	root := NewCommand()
	openCmd, _, err := root.Find([]string{"agent", "open"})
	require.NoError(t, err)
	for _, flag := range []string{"model", "effort", "permission-mode"} {
		_, ok := openCmd.GetFlagCompletionFunc(flag)
		assert.False(t, ok, "agent open must not register flag completion for --%s", flag)
	}

	for _, flag := range []string{"--model", "--effort", "--permission-mode"} {
		out := runTree(t, NewCommand(), "", "__complete", "agent", "open", "--provider", "codex", flag, "")
		require.Equal(t, cli.ExitOK, out.Code, "flag=%s stderr=%s", flag, out.Stderr)
		assert.NotContains(t, out.Stdout, "claude-opus", "flag=%s", flag)
		assert.NotContains(t, out.Stdout, "acceptEdits", "flag=%s", flag)
	}
	assert.Zero(t, disp.listAgentsCalls.Load(), "agent open option completion must not query ListAgents for self")
}

func TestControlCompletion_AdminCaptchaProviderUsesEnumNotAgentProviders(t *testing.T) {
	for _, verb := range []string{"set", "reset"} {
		out := runTree(t, NewCommand(), "", "__complete", "admin", "captcha", verb, "--provider", "")
		require.Equal(t, cli.ExitOK, out.Code, "verb=%s stderr=%s", verb, out.Stderr)
		assert.Contains(t, out.Stdout, "altcha\n", "verb=%s", verb)
		assert.Contains(t, out.Stdout, "recaptcha_v3\n", "verb=%s", verb)
		assert.Contains(t, out.Stdout, "turnstile\n", "verb=%s", verb)
		assert.NotContains(t, out.Stdout, "claude\n", "verb=%s", verb)
		assert.NotContains(t, out.Stdout, "codex\n", "verb=%s", verb)
	}
}

func TestControlCompletion_MigratedDomainFlagsRegistered(t *testing.T) {
	root := NewCommand()
	cases := []struct {
		path  []string
		flags []string
	}{
		{path: []string{"context", "use"}, flags: []string{"hub", "worker", "workspace", "provider"}},
		{path: []string{"workspace", "unarchive"}, flags: []string{"section"}},
		{path: []string{"workspace", "move"}, flags: []string{"section", "before", "after"}},
		{path: []string{"section", "move"}, flags: []string{"before", "after"}},
		{path: []string{"agent", "queue", "move"}, flags: []string{"before", "after", "workspace"}},
		{path: []string{"tab", "move"}, flags: []string{"before", "after", "tile", "workspace"}},
		{path: []string{"tab", "open-image"}, flags: []string{"before", "after", "tile", "workspace", "worker"}},
		{path: []string{"file", "open"}, flags: []string{"before", "after", "tile", "workspace", "worker"}},
		{path: []string{"terminal", "open"}, flags: []string{"shell", "base", "checkout", "use-worktree", "before", "after", "tile", "workspace", "worker"}},
		{path: []string{"git", "branch", "create"}, flags: []string{"base", "worker"}},
		{path: []string{"git", "diff"}, flags: []string{"base", "worker"}},
		{path: []string{"worktree", "create"}, flags: []string{"base", "worker"}},
		{path: []string{"agent", "list"}, flags: []string{"provider", "workspace", "worker", "parent"}},
		{path: []string{"agent", "open"}, flags: []string{"provider", "base", "checkout", "use-worktree", "before", "after", "tile", "workspace", "worker"}},
		{path: []string{"agent", "approve"}, flags: []string{"permission-mode", "workspace"}},
		{path: []string{"agent", "answer"}, flags: []string{"permission-mode", "workspace"}},
		{path: []string{"agent", "set"}, flags: []string{"model", "effort", "permission-mode", "workspace"}},
		{path: []string{"agent", "fork"}, flags: []string{"model", "effort", "permission-mode", "base", "checkout", "use-worktree", "before", "after", "tile", "workspace", "worker"}},
		{path: []string{"agent", "recoveries"}, flags: []string{"provider", "worker"}},
		{path: []string{"agent", "sessions"}, flags: []string{"provider", "worker"}},
		{path: []string{"agent", "search"}, flags: []string{"provider", "workspace", "worker"}},
		{path: []string{"agent", "retry-turn"}, flags: []string{"model", "workspace"}},
	}

	for _, tc := range cases {
		cmd, _, err := root.Find(tc.path)
		require.NoError(t, err, "path=%v", tc.path)
		for _, flag := range tc.flags {
			_, ok := cmd.GetFlagCompletionFunc(flag)
			assert.True(t, ok, "path=%v missing completion for --%s", tc.path, flag)
		}
	}

	// Verify live completion for provider flag and agent retry-turn / approve / answer option flags.
	provOut := runTree(t, NewCommand(), "", "__complete", "context", "use", "--provider", "clau")
	require.Equal(t, cli.ExitOK, provOut.Code, provOut.Stderr)
	assert.Contains(t, provOut.Stdout, "claude\n")

	disp := &optionCompletionDispatcher{}
	startSpawnIPC(t, &recordingHub{}, disp)
	t.Setenv(controlenv.TabIDVariable, "agent-1")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	for _, args := range [][]string{
		{"agent", "retry-turn", "self", "--model", "claude"},
		{"agent", "fork", "self", "--model", "claude"},
		{"agent", "approve", "self", "--permission-mode", "acc"},
		{"agent", "answer", "self", "--permission-mode", "acc"},
	} {
		out := runTree(t, NewCommand(), "", append([]string{"__complete"}, args...)...)
		require.Equal(t, cli.ExitOK, out.Code, "args=%v stderr=%s", args, out.Stderr)
		if args[len(args)-2] == "--model" {
			assert.Contains(t, out.Stdout, "claude-opus\tClaude Opus\n", "args=%v", args)
		} else {
			assert.Contains(t, out.Stdout, "acceptEdits\tAccept Edits\n", "args=%v", args)
		}
	}
}

func TestShortestUniquePrefixes_AndCompletionIDToken(t *testing.T) {
	assert.Empty(t, shortestUniquePrefixes(nil))
	assert.Equal(t, map[string]string{"short-1": "short-1"}, shortestUniquePrefixes([]string{"short-1"}))
	assert.Equal(t,
		map[string]string{"0123456789abcdef": "01234567"},
		shortestUniquePrefixes([]string{"0123456789abcdef", "0123456789abcdef"}),
	)

	// Shared prefix longer than 8 chars, short-ID collision, and full-ID prefix collision.
	ids := []string{
		"abcdefgh",
		"abcdefgh12345678",
		"sharedprefix_aaaa",
		"sharedprefix_aaab",
		"exactprefix12345",
		"exactprefix12345X",
	}
	got := shortestUniquePrefixes(ids)
	assert.Equal(t, "abcdefgh", got["abcdefgh"])
	assert.Equal(t, "abcdefgh1", got["abcdefgh12345678"])
	assert.Equal(t, "sharedprefix_aaaa", got["sharedprefix_aaaa"])
	assert.Equal(t, "sharedprefix_aaab", got["sharedprefix_aaab"])
	assert.Equal(t, "exactprefix12345", got["exactprefix12345"])
	assert.Equal(t, "exactprefix12345X", got["exactprefix12345X"])

	// completionIDToken keeps shortestTok while toComplete fits in it, and switches
	// to the full ID once toComplete exceeds shortestTok.
	const fullID = "0123456789abcdef1234"
	assert.Equal(t, "01234567", completionIDToken(fullID, "01234567", ""))
	assert.Equal(t, "01234567", completionIDToken(fullID, "01234567", "0123"))
	assert.Equal(t, "01234567", completionIDToken(fullID, "01234567", "01234567"))
	assert.Equal(t, fullID, completionIDToken(fullID, "01234567", "0123456789ab"))

	// End-to-end check on completeTargetsWithOptions with a typed prefix longer than 8 chars.
	hub := &recordingHub{
		workspaces: []*leapmuxv1.Workspace{
			{Id: fullID, Title: "Main"},
		},
	}
	startSpawnIPC(t, hub, parentCompletionDispatcher{})
	out := runTree(t, NewCommand(), "", "__complete", "workspace", "get", "0123456789ab")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.Contains(t, out.Stdout, fullID+"\tMain\n")
}

type targetMetadataDispatcher struct {
	tabTitles     map[string]string
	hasWorkerName bool
	workerName    string
}

func (d targetMetadataDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var listReq leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &listReq)
		var agents []*leapmuxv1.AgentInfo
		for _, id := range listReq.GetTabIds() {
			title, ok := d.tabTitles[id]
			if !ok {
				title = "Title " + id
			}
			agents = append(agents, &leapmuxv1.AgentInfo{Id: id, Title: title})
		}
		payload, _ := proto.Marshal(&leapmuxv1.ListAgentsResponse{Agents: agents})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	case leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure:
		name := "Box"
		if d.hasWorkerName {
			name = d.workerName
		}
		payload, _ := proto.Marshal(&leapmuxv1.GetWorkerSystemInfoResponse{Name: name})
		_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
	default:
		_ = w.SendError(12, req.GetMethod())
	}
}

func TestControlCompletion_SkipsUnrelatedWorkersBeforeE2EEFetch(t *testing.T) {
	t.Run("tabs filter by ID prefix before TabTitles", func(t *testing.T) {
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-alpha-1"},
					WorkerId:    "worker-alpha",
					WorkspaceId: "ws-1",
				},
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-beta-1"},
					WorkerId:    "worker-beta",
					WorkspaceId: "ws-1",
				},
			},
		}
		harness := startHubChannelHarness(t, hub, targetMetadataDispatcher{})

		// Non-matching ID prefix opens zero E2EE channels.
		out := runTree(t, NewCommand(), "", "__complete", "tab", "get", "--hub", harness.url, "zzz-none")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 0, harness.channelOpens())

		// Matching one tab opens only its hosting worker's channel.
		out = runTree(t, NewCommand(), "", "__complete", "tab", "get", "--hub", harness.url, "tab-alpha")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "tab-alpha-1\tTitle tab-alpha-1\n")
		assert.NotContains(t, out.Stdout, "tab-beta-1")
		assert.Equal(t, 1, harness.channelOpens())
	})

	t.Run("tabs fetch TabTitles for title prefix and omit empty or control-only labels", func(t *testing.T) {
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-alpha-1"},
					WorkerId:    "worker-alpha",
					WorkspaceId: "ws-1",
				},
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-beta-1"},
					WorkerId:    "worker-beta",
					WorkspaceId: "ws-1",
				},
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-empty-1"},
					WorkerId:    "worker-alpha",
					WorkspaceId: "ws-1",
				},
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-ctrl-1"},
					WorkerId:    "worker-beta",
					WorkspaceId: "ws-1",
				},
			},
		}
		disp := targetMetadataDispatcher{
			tabTitles: map[string]string{
				"tab-alpha-1": "\x1b[31mTitle\ttab-alpha-1\x1b[0m",
				"tab-beta-1":  "Title tab-beta-1",
				"tab-empty-1": "",
				"tab-ctrl-1":  "\x1b[31m\x07\u202e\x1b[0m",
			},
		}
		harness := startHubChannelHarness(t, hub, disp)

		out := runTree(t, NewCommand(), "", "__complete", "tab", "get", "--hub", harness.url, "ti")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 2, harness.channelOpens())
		assert.Contains(t, out.Stdout, "title:Title tab-alpha-1\t"+cli.ShortID("tab-alpha-1")+"\n")
		assert.Contains(t, out.Stdout, "title:Title tab-beta-1\t"+cli.ShortID("tab-beta-1")+"\n")
		assert.NotContains(t, out.Stdout, cli.ShortID("tab-empty-1"))
		assert.NotContains(t, out.Stdout, cli.ShortID("tab-ctrl-1"))
		assert.NotContains(t, out.Stdout, "title:\t")

		out = runTree(t, NewCommand(), "", "__complete", "tab", "get", "--hub", harness.url, "title:Title tab-alpha")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 4, harness.channelOpens())
		assert.Contains(t, out.Stdout, "title:Title tab-alpha-1\t"+cli.ShortID("tab-alpha-1")+"\n")
		assert.NotContains(t, out.Stdout, "title:Title tab-beta-1")
	})

	t.Run("tabs omit self when allowSelf is false with title prefix", func(t *testing.T) {
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-alpha-1"},
					WorkerId:    "worker-alpha",
					WorkspaceId: "ws-1",
				},
				{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-beta-1"},
					WorkerId:    "worker-beta",
					WorkspaceId: "ws-1",
				},
			},
		}
		harness := startHubChannelHarness(t, hub, targetMetadataDispatcher{})
		t.Setenv(controlenv.TabIDVariable, "tab-alpha-1")
		t.Setenv(controlenv.TabTypeVariable, "agent")

		out := runTree(t, NewCommand(), "", "__complete", "agent", "wait", "--hub", harness.url, "title:")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "title:Title tab-beta-1\t"+cli.ShortID("tab-beta-1")+"\n")
		assert.NotContains(t, out.Stdout, "title:Title tab-alpha-1")
		assert.NotContains(t, out.Stdout, "self\n")
	})

	t.Run("workers filter by ID prefix before WorkerNames", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-alpha", Online: true},
				{Id: "worker-beta", Online: true},
			},
		}
		harness := startHubChannelHarness(t, hub, targetMetadataDispatcher{})

		// Non-matching ID prefix opens zero E2EE channels.
		out := runTree(t, NewCommand(), "", "__complete", "worker", "get", "--hub", harness.url, "zzz-none")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 0, harness.channelOpens())

		// Matching one worker opens only that worker's channel.
		out = runTree(t, NewCommand(), "", "__complete", "worker", "get", "--hub", harness.url, "worker-alpha")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "worker-alpha\tBox\n")
		assert.NotContains(t, out.Stdout, "worker-beta")
		assert.Equal(t, 1, harness.channelOpens())
	})

	t.Run("workers fetch WorkerNames for name prefix and omit empty or control-only labels", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-alpha", Online: true},
				{Id: "worker-beta", Online: true},
			},
		}
		disp := targetMetadataDispatcher{
			hasWorkerName: true,
			workerName:    "\x1b[31mBox\tOne\x1b[0m",
		}
		harness := startHubChannelHarness(t, hub, disp)

		out := runTree(t, NewCommand(), "", "__complete", "worker", "get", "--hub", harness.url, "na")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 2, harness.channelOpens())
		assert.Contains(t, out.Stdout, "name:Box One\t"+cli.ShortID("worker-alpha")+"\n")
		assert.Contains(t, out.Stdout, "name:Box One\t"+cli.ShortID("worker-beta")+"\n")

		out = runTree(t, NewCommand(), "", "__complete", "worker", "get", "--hub", harness.url, "name:Box")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Equal(t, 4, harness.channelOpens())
		assert.Contains(t, out.Stdout, "name:Box One\t"+cli.ShortID("worker-alpha")+"\n")
		assert.Contains(t, out.Stdout, "name:Box One\t"+cli.ShortID("worker-beta")+"\n")

		for _, tc := range []struct {
			name  string
			label string
		}{
			{name: "empty worker label", label: ""},
			{name: "control-only worker label", label: "\x1b[31m\x07\u202e\x1b[0m"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				blankHub := &recordingHub{
					workers: []*leapmuxv1.Worker{{Id: "worker-blank", Online: true}},
				}
				blankDisp := targetMetadataDispatcher{
					hasWorkerName: true,
					workerName:    tc.label,
				}
				blankHarness := startHubChannelHarness(t, blankHub, blankDisp)

				blankOut := runTree(t, NewCommand(), "", "__complete", "worker", "get", "--hub", blankHarness.url, "na")
				require.Equal(t, cli.ExitOK, blankOut.Code, blankOut.Stderr)
				assert.Equal(t, 1, blankHarness.channelOpens())
				assert.NotContains(t, blankOut.Stdout, "name:")
				assert.NotContains(t, blankOut.Stdout, cli.ShortID("worker-blank"))
			})
		}
	})
}

func TestResolveCompletionAgent_ReusesCanonicalTabAndWorkerID(t *testing.T) {
	t.Run("prefix match reuses t.Tab WorkerId without calling LocateTab", func(t *testing.T) {
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-prefix-123456"},
				WorkerId:    "worker-B",
				WorkspaceId: "ws-1",
			}},
		}
		startSpawnIPC(t, hub, parentCompletionDispatcher{})
		peer, err := control.NewClientFromEnv("")
		require.NoError(t, err)

		tabID, workerID, ok := resolveCompletionAgent(t.Context(), &cobra.Command{}, peer, "agent-prefix")
		require.True(t, ok)
		assert.Equal(t, "agent-prefix-123456", tabID)
		assert.Equal(t, "worker-B", workerID)
		assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	})

	t.Run("self via WorkerIPC Whoami reuses t.WorkerID without calling LocateTab", func(t *testing.T) {
		hub := &recordingHub{}
		startSpawnIPC(t, hub, parentCompletionDispatcher{})
		// Clear ambient tab/worker env vars so resolve.Canonical uses IPC Whoami.
		t.Setenv(controlenv.TabIDVariable, "")
		t.Setenv(controlenv.TabTypeVariable, "")
		t.Setenv(controlenv.WorkerIDVariable, "")

		peer, err := control.NewClientFromEnv("")
		require.NoError(t, err)

		tabID, workerID, ok := resolveCompletionAgent(t.Context(), &cobra.Command{}, peer, resolve.SelfTarget)
		require.True(t, ok)
		assert.Equal(t, "agent-1", tabID)
		assert.Equal(t, "worker-A", workerID)
		assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
	})

	t.Run("blank agent arg returns false without dialing", func(t *testing.T) {
		hub := &recordingHub{}
		startSpawnIPC(t, hub, parentCompletionDispatcher{})
		t.Setenv(controlenv.TabIDVariable, "agent-1")
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		peer, err := control.NewClientFromEnv("")
		require.NoError(t, err)

		for _, blank := range []string{"", "   ", "\t"} {
			tabID, workerID, ok := resolveCompletionAgent(t.Context(), &cobra.Command{}, peer, blank)
			assert.False(t, ok, "blank=%q must return ok=false", blank)
			assert.Empty(t, tabID)
			assert.Empty(t, workerID)
		}
		assert.Empty(t, hub.called())
	})
}

func TestCompletionWorkerID_AmbientTabFallbackAndNoAmbiguityFanout(t *testing.T) {
	t.Run("resolves ambient tab worker via LocateTab when worker env is empty", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-alpha", Online: true},
				{Id: "worker-beta", Online: true},
			},
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-ambient-1"},
				WorkerId:    "worker-beta",
				WorkspaceId: "ws-1",
			},
		}
		harness := startHubChannelHarness(t, hub, targetMetadataDispatcher{})
		t.Setenv(controlenv.WorkerIDVariable, "")
		t.Setenv(controlenv.TabIDVariable, "tab-ambient-1")

		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)

		workerID, err := completionWorkerID(t.Context(), &cobra.Command{}, peer)
		require.NoError(t, err)
		assert.Equal(t, "worker-beta", workerID)
		assert.Contains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure)
		assert.Equal(t, 0, harness.channelOpens())
	})

	t.Run("returns empty without E2EE fan-out when multiple workers are online", func(t *testing.T) {
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-alpha", Online: true},
				{Id: "worker-beta", Online: true},
			},
		}
		harness := startHubChannelHarness(t, hub, targetMetadataDispatcher{})
		t.Setenv(controlenv.WorkerIDVariable, "")
		t.Setenv(controlenv.TabIDVariable, "")

		peer, err := control.NewClientFromEnv(harness.url)
		require.NoError(t, err)

		workerID, err := completionWorkerID(t.Context(), &cobra.Command{}, peer)
		require.NoError(t, err)
		assert.Equal(t, "", workerID)
		assert.Equal(t, 0, harness.channelOpens())
	})
}

func TestCompletionWorkerID_MemoizesListAllWorkers(t *testing.T) {
	hub := &recordingHub{
		workers: []*leapmuxv1.Worker{
			{Id: "worker-online", Online: true},
		},
	}
	startSpawnIPC(t, hub, parentCompletionDispatcher{})
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv(controlenv.WorkerIDVariable, "")
	require.NoError(t, control.SaveContext(control.ContextConfig{Worker: "worker-gone"}))

	peer, err := control.NewClientFromEnv("")
	require.NoError(t, err)

	workerID, err := completionWorkerID(t.Context(), &cobra.Command{}, peer)
	require.NoError(t, err)
	assert.Equal(t, "worker-online", workerID)

	listCalls := 0
	for _, proc := range hub.called() {
		if proc == leapmuxv1connect.WorkerManagementServiceListWorkersProcedure {
			listCalls++
		}
	}
	assert.Equal(t, 1, listCalls, "stale saved worker fallback must reuse the fetched worker list")
}

func TestCompletionGitDir_SkipsCwdFallbackForRemoteWorker(t *testing.T) {
	t.Setenv(controlenv.WorkingDirVariable, "")
	cwd, err := os.Getwd()
	require.NoError(t, err)

	remoteHub := &control.HubClient{HubURL: "https://remote.example.invalid"}
	assert.Equal(t, "", completionGitDir(&cobra.Command{}, remoteHub, "worker-remote"))

	localHub := &control.HubClient{HubURL: "unix:/tmp/leapmux-local.sock"}
	assert.Equal(t, cwd, completionGitDir(&cobra.Command{}, localHub, "worker-local"))

	cmdWithWorker := &cobra.Command{}
	cmdWithWorker.Flags().String(workerFlag, "", "")
	require.NoError(t, cmdWithWorker.Flags().Set(workerFlag, "worker-other"))
	assert.Equal(t, "", completionGitDir(cmdWithWorker, localHub, "worker-other"))

	ipcPeer := &control.WorkerIPCClient{SocketURL: "unix:/tmp/ipc.sock"}
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	assert.Equal(t, cwd, completionGitDir(&cobra.Command{}, ipcPeer, "worker-A"))
	assert.Equal(t, "", completionGitDir(&cobra.Command{}, ipcPeer, "worker-B"))

	// Explicit --dir always wins, even for remote workers.
	cmdWithDir := &cobra.Command{}
	cmdWithDir.Flags().String("dir", "", "")
	require.NoError(t, cmdWithDir.Flags().Set("dir", "/srv/repo"))
	assert.Equal(t, "/srv/repo", completionGitDir(cmdWithDir, remoteHub, "worker-remote"))
}

func TestCompletionGitDir_DoesNotUseTabWorkingDirForAnotherWorker(t *testing.T) {
	ipcPeer := &control.WorkerIPCClient{SocketURL: "unix:/tmp/ipc.sock"}
	localHub := &control.HubClient{HubURL: "unix:/tmp/leapmux-local.sock"}

	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	t.Setenv(controlenv.WorkingDirVariable, "/local/tab")

	// Same worker receives the tab working directory.
	assert.Equal(t, "/local/tab", completionGitDir(&cobra.Command{}, ipcPeer, "worker-A"))

	// Targeting another worker must not leak worker-A's working directory.
	cmdWorkerB := &cobra.Command{}
	cmdWorkerB.Flags().String(workerFlag, "", "")
	require.NoError(t, cmdWorkerB.Flags().Set(workerFlag, "worker-B"))
	assert.Equal(t, "", completionGitDir(cmdWorkerB, ipcPeer, "worker-B"))
	assert.Equal(t, "", completionGitDir(&cobra.Command{}, ipcPeer, "worker-B"))
	assert.Equal(t, "", completionGitDir(cmdWorkerB, localHub, "worker-B"))

	// Explicit --dir still applies when targeting another worker.
	cmdWorkerBWithDir := &cobra.Command{}
	cmdWorkerBWithDir.Flags().String(workerFlag, "", "")
	cmdWorkerBWithDir.Flags().String("dir", "", "")
	require.NoError(t, cmdWorkerBWithDir.Flags().Set(workerFlag, "worker-B"))
	require.NoError(t, cmdWorkerBWithDir.Flags().Set("dir", "/remote/repo"))
	assert.Equal(t, "/remote/repo", completionGitDir(cmdWorkerBWithDir, ipcPeer, "worker-B"))

	// Without LEAPMUX_CONTROL_WORKER_ID, an explicit --worker also suppresses
	// the ambient tab working directory.
	t.Setenv(controlenv.WorkerIDVariable, "")
	assert.Equal(t, "/local/tab", completionGitDir(&cobra.Command{}, localHub, "worker-A"))
	assert.Equal(t, "", completionGitDir(cmdWorkerB, localHub, "worker-B"))
}

type stubAdminUserCompletionService struct {
	leapmuxv1connect.UnimplementedAdminUserServiceHandler

	users []*leapmuxv1.AdminUser
	err   error
}

func (s *stubAdminUserCompletionService) ListUsers(_ context.Context, _ *leapmuxv1.ListUsersRequest) (*leapmuxv1.ListUsersResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &leapmuxv1.ListUsersResponse{Users: s.users}, nil
}

func newAdminCompletionTestCmd(t *testing.T, hubURL string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("hub", hubURL, "")
	require.NoError(t, cmd.Flags().Set("hub", hubURL))
	return cmd
}

func TestAdminCompletionHelpers(t *testing.T) {
	t.Run("completeAdminItems sanitizes, filters, sorts, formats, and handles errors", func(t *testing.T) {
		isolateCLIEnv(t)
		const hub = "https://admin-items.example.invalid"
		require.NoError(t, control.SaveCredentials(hub, control.CredentialFile{
			HubURL:      hub,
			AccessToken: "tok",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		cmd := newAdminCompletionTestCmd(t, hub)

		cands, dir := completeAdminItems(cmd, "al", func(_ context.Context, _ *control.HubClient) ([]completionItem, error) {
			return []completionItem{
				{value: "\x1b[31malpha-2\x1b[0m", desc: "second\titem\x1b[0m"},
				{value: "alpha-1", desc: "first"},
				{value: "alpha-nodescription", desc: "\x1b[31m\x1b[0m"},
				{value: "beta-1", desc: "filtered out"},
				{value: "", desc: "empty value"},
				{value: "\x1b[31m\x07\u202e\x1b[0m", desc: "control only"},
			}, nil
		})
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Equal(t, []string{
			"alpha-1\tfirst",
			"alpha-2\tsecond item",
			"alpha-nodescription",
		}, cands)

		errCands, errDir := completeAdminItems(cmd, "", func(_ context.Context, _ *control.HubClient) ([]completionItem, error) {
			return nil, errors.New("rpc failed")
		})
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, errDir)
		assert.Nil(t, errCands)
	})

	t.Run("completeAdminList maps rows, sanitizes, filters, sorts, and handles errors", func(t *testing.T) {
		isolateCLIEnv(t)
		const hub = "https://admin-list.example.invalid"
		require.NoError(t, control.SaveCredentials(hub, control.CredentialFile{
			HubURL:      hub,
			AccessToken: "tok",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		cmd := newAdminCompletionTestCmd(t, hub)

		type row struct {
			id   string
			desc string
		}
		cands, dir := completeAdminList(cmd, "k-",
			func(_ context.Context, _ *control.HubClient) ([]row, error) {
				return []row{
					{id: "\x1b[31mk-2\x1b[0m", desc: "two\tline"},
					{id: "k-1", desc: "one"},
					{id: "other-1", desc: "skip"},
					{id: "\x1b[31m\x07\x1b[0m", desc: "drop"},
				}, nil
			},
			func(r row) (string, string) { return r.id, r.desc },
		)
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Equal(t, []string{"k-1\tone", "k-2\ttwo line"}, cands)

		errCands, errDir := completeAdminList(cmd, "",
			func(_ context.Context, _ *control.HubClient) ([]row, error) {
				return nil, errors.New("list failed")
			},
			func(r row) (string, string) { return r.id, r.desc },
		)
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, errDir)
		assert.Nil(t, errCands)
	})

	t.Run("completeAdminUsers emits username and id entries, skips blank or control-only values, and handles RPC errors", func(t *testing.T) {
		svc := &stubAdminUserCompletionService{
			users: []*leapmuxv1.AdminUser{
				{Id: "u-2", Username: "zoe", DisplayName: "Zoe\nSmith"},
				{Id: "u-1", Username: "\x1b[31malice\x1b[0m", DisplayName: "Alice\tOne"},
				{Id: "u-no-name", Username: "", DisplayName: "Nameless"},
				{Id: "", Username: "no-id-user", DisplayName: "No ID"},
				{Id: "", Username: "\x1b[31m\x07\u202e\x1b[0m", DisplayName: "Control Only"},
				{Id: "u-ctrl-name", Username: "\x1b[31m\x1b[0m", DisplayName: "Dropped"},
			},
		}
		connectServer := connect.NewServer()
		leapmuxv1connect.RegisterAdminUserServiceHandler(connectServer, svc)
		mux := http.NewServeMux()
		connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
		srv := hubtransporttest.NewServer(t, mux)
		t.Cleanup(srv.Close)

		isolateCLIEnv(t)
		require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
			HubURL:      srv.URL,
			AccessToken: "lmx_a_at_admin",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		cmd := newAdminCompletionTestCmd(t, srv.URL)

		all, dir := completeAdminUsers(cmd, cli.ArgSpec{}, "")
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Equal(t, []string{
			"alice\tAlice One",
			"id:u-1\talice",
			"id:u-2\tzoe",
			"id:u-ctrl-name",
			"id:u-no-name",
			"no-id-user\tNo ID",
			"zoe\tZoe Smith",
		}, all)

		filtered, dir := completeAdminUsers(cmd, cli.ArgSpec{}, "id:u-1")
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		assert.Equal(t, []string{"id:u-1\talice"}, filtered)

		svc.err = connect.NewError(connect.CodePermissionDenied, "admin scope required")
		errCands, errDir := completeAdminUsers(cmd, cli.ArgSpec{}, "")
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, errDir)
		assert.Nil(t, errCands)
	})
}

func TestMergeFlagCompletions(t *testing.T) {
	fnA := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"a"}, cobra.ShellCompDirectiveNoFileComp
	}
	fnB := func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"b"}, cobra.ShellCompDirectiveNoFileComp
	}

	merged := mergeFlagCompletions(
		nil,
		map[string]cobra.CompletionFunc{},
		map[string]cobra.CompletionFunc{"alpha": fnA},
		map[string]cobra.CompletionFunc{"beta": fnB},
	)
	require.Len(t, merged, 2)
	gotA, dirA := merged["alpha"](nil, nil, "")
	assert.Equal(t, []string{"a"}, gotA)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dirA)
	gotB, dirB := merged["beta"](nil, nil, "")
	assert.Equal(t, []string{"b"}, gotB)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dirB)

	assert.PanicsWithValue(t, `cmd: duplicate flag completion registration for "alpha"`, func() {
		mergeFlagCompletions(
			map[string]cobra.CompletionFunc{"alpha": fnA},
			map[string]cobra.CompletionFunc{"alpha": fnB},
		)
	})
}
