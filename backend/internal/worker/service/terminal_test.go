package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// TestSucceedTerminalStartup_ListTerminalsReportsTheReadyThatTheClientReceived
// pins the order of succeedTerminalStartup: when a client receives READY,
// ListTerminals already reports it.
//
// The order used to be persist, broadcast, registry, which is the defect that
// failStartup had. ListTerminals lets the registry override the READY of a
// live terminal, so a client that reloads between the broadcast and the
// registry write shows a startup that never ends.
func TestSucceedTerminalStartup_ListTerminalsReportsTheReadyThatTheClientReceived(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := testutil.DeadlineContext(t)
	workingDir := t.TempDir()
	release := make(chan struct{})
	svc.startTerminalFn = func(startCtx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		<-release
		return svc.Terminals.StartTerminal(startCtx, opts, outFn, exitFn)
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir: workingDir,
		Shell:      testutil.TestShell(),
	}, w)
	ids := collectTerminalIDs(w)
	require.Len(t, ids, 1)
	terminalID := ids[0]
	// After the t.TempDir above, so the LIFO cleanup stops the shell before it
	// removes the shell's working directory.
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)

	listed := make(chan *testResponseWriter, 1)
	registerTerminalWatch(svc, testChannelID, terminalID, leapmuxv1.WatchMode_WATCH_MODE_FULL, &finalStatusReader{
		testResponseWriter: newTestWriter(),
		isFinal: func(resp *leapmuxv1.WatchEventsResponse) bool {
			return resp.GetTerminalEvent().GetStatusChange().GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		},
		read: func() {
			lw := newTestWriter()
			dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, lw)
			listed <- lw
		},
	})
	close(release)

	lw := awaitListed(t, ctx, listed)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(lw.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	// Compared as names, so a failure states STARTING rather than a number.
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY.String(), resp.GetTerminals()[0].GetStatus().String(),
		"the client received READY, and ListTerminals still reported the startup in progress")
}
