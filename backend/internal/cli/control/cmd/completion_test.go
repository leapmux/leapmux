package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestControlCompletionCompletesScopeFlagValues(t *testing.T) {
	hub, _ := startSelfRenameIPC(t)
	out := runTree(t, NewCommand(), "", "__complete", "control", "tab", "list", "--workspace", "ws")
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
		out := runTree(t, NewCommand(), "", append([]string{"__complete", "control"}, tc.args...)...)
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, tc.want)
		assert.NotContains(t, out.Stdout, "working\n")
	}
}

func TestControlCompletionDoesNotQueryTheTabSocketWithABlankHub(t *testing.T) {
	for _, value := range []string{"", " \t"} {
		hub, _ := startSelfRenameIPC(t)
		out := runTree(t, NewCommand(), "", "__complete", "control", "tab", "get", "--hub", value, "agent")
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
	require.NoError(t, pins.Verify("spare-worker", []byte("x25519"), []byte("mlkem"), []byte("slhdsa")))
	for _, verb := range []string{"get", "remove"} {
		out := runTree(t, NewCommand(), "", "__complete", "control", "worker", "pins", verb, "--hub", hub, "spare")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "spare-worker\n")
		assert.NotContains(t, out.Stdout, "self\n")
	}
}

func TestControlCompletionOmitsTheCallingAgentFromWaitTargets(t *testing.T) {
	startSelfRenameIPC(t)
	out := runTree(t, NewCommand(), "", "__complete", "control", "agent", "wait", "")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.NotContains(t, out.Stdout, "agent-1")
	assert.NotContains(t, out.Stdout, "self\n")
}
