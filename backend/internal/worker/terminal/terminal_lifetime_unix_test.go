//go:build unix

package terminal

import (
	"context"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil/processtest"
	"github.com/stretchr/testify/require"
)

func TestHelperOwnedProcessTree(*testing.T) { processtest.RunHelper() }

func startTerminalTree(t *testing.T, home, id string) (*Terminal, *processtest.Tree, context.CancelFunc) {
	t.Helper()
	tree := processtest.New(t, home)
	ctx, cancel := context.WithCancel(t.Context())
	terminal, err := Start(ctx, Options{ID: id, Shell: tree.Shell(t), WorkingDir: t.TempDir(), ExtraEnv: tree.Environment()}, quartz.NewReal(), func([]byte, int64, []Signal) {})
	require.NoError(t, err)
	t.Cleanup(func() { terminal.Stop(); cancel() })
	tree.Ready(t, terminal.ShellPID())
	return terminal, tree, cancel
}

func waitForLifetimeTerminal(t *testing.T, terminal *Terminal) {
	t.Helper()
	select {
	case <-terminal.exitCh:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the owned terminal root did not exit")
	}
}

func TestTerminalStopEndsOnlyItsDetachedChild(t *testing.T) {
	home := t.TempDir()
	first, firstTree, _ := startTerminalTree(t, home, "first-owned-terminal")
	_, secondTree, _ := startTerminalTree(t, home, "second-owned-terminal")
	firstTree.RequirePong(t)
	secondTree.RequirePong(t)
	first.Stop()
	waitForLifetimeTerminal(t, first)
	secondTree.RequirePong(t)
	firstTree.RequireChildClosed(t)
}

func TestTerminalContextCancellationEndsOnlyItsDetachedChild(t *testing.T) {
	home := t.TempDir()
	first, firstTree, cancel := startTerminalTree(t, home, "first-cancelled-terminal")
	_, secondTree, _ := startTerminalTree(t, home, "second-live-terminal")
	firstTree.RequirePong(t)
	secondTree.RequirePong(t)
	cancel()
	waitForLifetimeTerminal(t, first)
	secondTree.RequirePong(t)
	firstTree.RequireChildClosed(t)
}
