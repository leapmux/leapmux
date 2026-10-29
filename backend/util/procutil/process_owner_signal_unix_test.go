//go:build !windows

package procutil

import (
	"bufio"
	"io"
	"os/exec"
	"syscall"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/require"
)

type groupSignalFixture struct {
	owner  *ProcessOwner
	input  io.WriteCloser
	output *bufio.Reader
}

func newGroupSignalFixture(t *testing.T) *groupSignalFixture {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/cat")
	DetachFromTerminal(cmd)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Stdin.(io.Closer).Close() })
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close(); _ = cmd.Stdout.(io.Closer).Close() })
	owner := PrepareProcess(cmd)
	fixture := &groupSignalFixture{owner: owner, input: input, output: bufio.NewReader(output)}
	var identity ProcessIdentity
	t.Cleanup(func() {
		owner.mu.Lock()
		if !identity.IsZero() {
			owner.root = identity
		}
		owner.mu.Unlock()
		_ = input.Close()
		_ = owner.Wait()
		_ = owner.Close()
	})
	require.NoError(t, owner.Start())
	identity = owner.root
	fixture.requireEcho(t, "ready\n")
	return fixture
}

func (f *groupSignalFixture) requireEcho(t *testing.T, text string) {
	t.Helper()
	_, err := io.WriteString(f.input, text)
	require.NoError(t, err)
	type response struct {
		line string
		err  error
	}
	read := make(chan response, 1)
	go func() {
		line, err := f.output.ReadString('\n')
		read <- response{line: line, err: err}
	}()
	select {
	case result := <-read:
		require.NoError(t, result.err)
		require.Equal(t, text, result.line)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the signal fixture did not echo its input")
	}
}

func TestProcessOwnerSignalsNoGroupWithAStaleIdentity(t *testing.T) {
	t.Parallel()
	fixture := newGroupSignalFixture(t)
	fixture.owner.mu.Lock()
	fixture.owner.root.StartTime++
	fixture.owner.mu.Unlock()
	require.NoError(t, fixture.owner.SignalGroup(syscall.SIGTERM))
	fixture.requireEcho(t, "still running\n")
}

func TestProcessOwnerSignalGroupRequiresAnActiveOwner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		block func(*ProcessOwner)
	}{
		{label: "unstarted", block: func(owner *ProcessOwner) { owner.started = false }},
		{label: "closed", block: func(owner *ProcessOwner) { owner.closed = true }},
		{label: "terminating", block: func(owner *ProcessOwner) { owner.terminating = true }},
		{label: "wait completed", block: func(owner *ProcessOwner) { owner.waitFinished = true }},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			fixture := newGroupSignalFixture(t)
			owner := fixture.owner
			owner.mu.Lock()
			started, closed, terminating, finished := owner.started, owner.closed, owner.terminating, owner.waitFinished
			tc.block(owner)
			owner.mu.Unlock()
			t.Cleanup(func() {
				owner.mu.Lock()
				owner.started, owner.closed, owner.terminating, owner.waitFinished = started, closed, terminating, finished
				owner.mu.Unlock()
			})
			require.NoError(t, owner.SignalGroup(syscall.SIGTERM))
			fixture.requireEcho(t, "still owned\n")
		})
	}
}

func TestProcessOwnerSignalGroupDeliversToALiveGroup(t *testing.T) {
	t.Parallel()
	fixture := newGroupSignalFixture(t)
	require.NoError(t, fixture.owner.SignalGroup(syscall.SIGTERM))
	exited := make(chan error, 1)
	go func() { exited <- fixture.owner.Wait() }()
	select {
	case err := <-exited:
		require.Error(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the owned group did not exit after its signal")
	}
	status := fixture.owner.ProcessState().Sys().(syscall.WaitStatus)
	require.True(t, status.Signaled())
	require.Equal(t, syscall.SIGTERM, status.Signal())
	require.NoError(t, fixture.owner.SignalGroup(syscall.SIGTERM))
}

func TestProcessOwnerSignalGroupReturnsDeliveryErrorsWithoutEndingTheOwner(t *testing.T) {
	t.Parallel()
	fixture := newGroupSignalFixture(t)
	for _, signal := range []syscall.Signal{-1, 1 << 30} {
		require.ErrorIs(t, fixture.owner.SignalGroup(signal), syscall.EINVAL)
		fixture.requireEcho(t, "fallback remains available\n")
		require.NoError(t, fixture.owner.Err())
	}
}
