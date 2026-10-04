package procutil

import (
	"context"
	"os"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/require"
)

func TestPreparePTYProcessRejectsAbsentContextTerminalAndProgram(t *testing.T) {
	var absentContext context.Context
	_, err := PreparePTYProcess(absentContext, nil, PTYLaunch{})
	require.ErrorContains(t, err, "context is absent")
	_, err = PreparePTYProcess(t.Context(), nil, PTYLaunch{Program: "unused"})
	require.ErrorContains(t, err, "terminal and program")
	terminal, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminal.Close() })
	_, err = PreparePTYProcess(t.Context(), terminal, PTYLaunch{})
	require.ErrorContains(t, err, "terminal and program")
}

func TestPreparePTYProcessKeepsCancellationBeforeStartup(t *testing.T) {
	terminal, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminal.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	owner, err := PreparePTYProcess(ctx, terminal, PTYLaunch{Program: os.Args[0]})
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, owner.Start(), context.Canceled)
	require.Zero(t, owner.PID())
	require.ErrorIs(t, owner.Close(), context.Canceled)
	require.ErrorIs(t, owner.Close(), context.Canceled)
}

func TestPreparePTYProcessKeepsAnAlreadyCanceledContextForStart(t *testing.T) {
	terminal, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminal.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	owner, err := PreparePTYProcess(ctx, terminal, PTYLaunch{Program: os.Args[0]})
	require.NoError(t, err, "construction grants no process ownership before Start")
	require.ErrorIs(t, owner.Start(), context.Canceled)
	require.Zero(t, owner.PID())
	require.ErrorIs(t, owner.Close(), context.Canceled)
}
