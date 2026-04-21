package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// TestResizeTerminal_DuringStartup_LandsOnPTY pins the bug where a
// ResizeTerminal RPC that arrives while the PTY is still being spawned is
// silently dropped, leaving vim/nvim stuck at the hardcoded 80x24 that
// OpenTerminal passed even when the user's viewport is much larger.
//
// The sync prologue of OpenTerminal returns immediately and the PTY is
// spawned in a background goroutine, so the window where the terminal
// exists as a "tab" but not yet as a Manager-registered PTY is exactly
// when the frontend's first fit() fires. Before the fix the handler
// returned an error that the frontend swallowed, and nothing re-sent
// the dims, so the PTY stayed 80x24 for the session.
//
// Contract: a ResizeTerminal arriving during startup must be stashed and
// applied to the PTY the moment StartTerminal registers it in the
// Manager, so the frontend's size is visible to the first process that
// queries TIOCGWINSZ (e.g. vim on its first draw).
func TestResizeTerminal_DuringStartup_LandsOnPTY(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")

	// Gate the real StartTerminal behind a channel so the test can
	// dispatch ResizeTerminal while the goroutine is parked with the
	// PTY not yet registered.
	enteredStart := make(chan struct{})
	proceed := make(chan struct{})
	var gateOnce sync.Once
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		gateOnce.Do(func() { close(enteredStart) })
		select {
		case <-proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
		return svc.Terminals.StartTerminal(ctx, opts, outFn, exitFn)
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       testutil.TestShell(),
		Cols:        80,
		Rows:        24,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	// Wait for runTerminalStartup to reach startTerminalFn — past this
	// point the tab is STARTING and the Manager does not yet hold the
	// PTY, exactly the window this test covers.
	select {
	case <-enteredStart:
	case <-time.After(5 * time.Second):
		t.Fatal("startTerminalFn never invoked — runTerminalStartup did not reach PTY spawn")
	}

	wResize := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "ResizeTerminal", &leapmuxv1.ResizeTerminalRequest{
		TerminalId: terminalID,
		Cols:       180,
		Rows:       50,
	}, wResize)
	require.Empty(t, wResize.errors,
		"ResizeTerminal during STARTING must succeed (stash for later apply), not error out")
	require.Len(t, wResize.responses, 1)

	// Unblock the gated StartTerminal so the real Manager registers the
	// PTY; runTerminalStartup then applies the stashed dims.
	close(proceed)

	// Clean up the real PTY regardless of test outcome.
	t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })

	require.Eventually(t, func() bool {
		meta, ok := svc.Terminals.GetMeta(terminalID)
		if !ok {
			return false
		}
		return meta.Cols == 180 && meta.Rows == 50
	}, 5*time.Second, 20*time.Millisecond,
		"pending resize stashed during STARTING must land on the PTY after startup — "+
			"vim would otherwise see 80x24 on its first TIOCGWINSZ query")
}
