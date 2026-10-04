//go:build unix

package acp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil/processtest"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/require"
)

func TestHelperOwnedProcessTree(*testing.T) { processtest.RunHelper() }

func startACPCommandTree(t *testing.T, home string) (*processtest.Tree, context.CancelFunc, <-chan error) {
	t.Helper()
	tree := processtest.New(t, home)
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, os.Args[0], processtest.Arguments()...)
	cmd.Env = append(os.Environ(), tree.Environment()...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	configureACPTerminalCmd(cmd)
	owner := procutil.PrepareProcess(cmd)
	require.NoError(t, owner.Start())
	finished := make(chan error, 1)
	go func() { finished <- owner.Wait() }()
	t.Cleanup(cancel)
	tree.Ready(t, cmd.Process.Pid)
	return tree, cancel, finished
}

func TestACPTerminalContextCancellationEndsOnlyItsDetachedChild(t *testing.T) {
	home := t.TempDir()
	first, cancel, finished := startACPCommandTree(t, home)
	second, _, _ := startACPCommandTree(t, home)
	first.RequirePong(t)
	second.RequirePong(t)
	cancel()
	select {
	case <-finished:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the cancelled ACP terminal root did not exit")
	}
	second.RequirePong(t)
	first.RequireChildClosed(t)
}

func startACPHostTree(t *testing.T, base *Base, recorder *responseRecorder, home string, requestID int) (*acpTerminalSession, *processtest.Tree) {
	t.Helper()
	tree := processtest.New(t, home)
	variables := make([]acpTerminalEnvVar, 0)
	for _, value := range tree.Environment() {
		name, text, _ := strings.Cut(value, "=")
		variables = append(variables, acpTerminalEnvVar{Name: name, Value: text})
	}
	dispatchTerminal(base, acpMethodTerminalCreate, requestID, acpTerminalCreateParams{SessionID: "sess-1", Command: os.Args[0], Args: processtest.Arguments(), Cwd: base.workingDir, Env: variables})
	responses := recorder.wait(t, requestID, 30*time.Second)
	response := responses[requestID-1]
	require.Nil(t, response["error"])
	result, valid := response["result"].(map[string]any)
	require.True(t, valid)
	id, valid := result["terminalId"].(string)
	require.True(t, valid)
	base.terminalsMu.Lock()
	session := base.terminals[id]
	base.terminalsMu.Unlock()
	require.NotNil(t, session)
	t.Cleanup(func() { session.kill(); session.waitDone(acpTerminalReleaseWait) })
	tree.Ready(t, session.owner.PID())
	return session, tree
}

func TestACPTerminalKillEndsOnlyItsDetachedChild(t *testing.T) {
	base, recorder := newTerminalTestBase(t, &agenttest.Sink{})
	home := t.TempDir()
	first, firstTree := startACPHostTree(t, base, recorder, home, 1)
	_, secondTree := startACPHostTree(t, base, recorder, home, 2)
	firstTree.RequirePong(t)
	secondTree.RequirePong(t)
	dispatchTerminal(base, acpMethodTerminalKill, 3, acpTerminalIDParams{SessionID: "sess-1", TerminalID: first.id})
	responses := recorder.wait(t, 3, 30*time.Second)
	require.Nil(t, responses[2]["error"])
	select {
	case <-first.done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the killed ACP terminal root did not exit")
	}
	secondTree.RequirePong(t)
	firstTree.RequireChildClosed(t)
}
