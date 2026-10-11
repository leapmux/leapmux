//go:build unix

package codewhale

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil/processtest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/require"
)

func TestHelperOwnedProcessTree(*testing.T) { processtest.RunHelper() }

func startCodewhaleOrphanTree(t *testing.T, home string) (*exec.Cmd, int, *processtest.Tree, <-chan error) {
	t.Helper()
	tree := processtest.New(t, home)
	port, err := providerkit.ReserveLoopbackPort()
	require.NoError(t, err)
	args := append(processtest.Arguments(), "app-server", "--port", strconv.Itoa(port))
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), tree.Environment()...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	procutil.DetachFromTerminal(cmd)
	require.NoError(t, cmd.Start())
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	tree.Ready(t, cmd.Process.Pid)
	return cmd, port, tree, finished
}

func TestReclaimStoreEndsOnlyItsVerifiedRuntimeAndDetachedChild(t *testing.T) {
	home := t.TempDir()
	first, port, firstTree, finished := startCodewhaleOrphanTree(t, home)
	_, _, secondTree, _ := startCodewhaleOrphanTree(t, home)
	firstTree.RequirePong(t)
	secondTree.RequirePong(t)
	store := codewhaleStore{dir: t.TempDir()}
	owner := runtimeOwner(t, first, port)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	require.NoError(t, reclaimStore(context.Background(), store, quartz.NewReal()))
	select {
	case <-finished:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the reclaimed runtime root did not exit")
	}
	secondTree.RequirePong(t)
	firstTree.RequireChildClosed(t)
}
