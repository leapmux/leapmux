//go:build unix

package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// listTerminalsDuringGit runs a ListTerminals call for one terminal and holds it
// inside its git status. It calls change while git runs, then releases git and
// returns the terminal of the reply.
//
// The reply leaves the Worker after git ends. A browser takes the reply as the
// state of the terminal, so the reply must describe the terminal when the reply
// is built. A state that the call read before git is older than every event that
// the Worker broadcast during git, and a browser that applied such an event then
// takes the older reply over it.
func listTerminalsDuringGit(t *testing.T, d *channel.Dispatcher, terminalID string, change func()) *leapmuxv1.TerminalInfo {
	t.Helper()
	gitStarted, gitRelease := installBlockingGit(t)
	w := newTestWriter()
	listed := make(chan struct{})
	go func() {
		defer close(listed)
		dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, w)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(gitStarted); return err == nil }, inputQueueWait, 10*time.Millisecond,
		"ListTerminals must reach the git status of its terminal")

	change()
	require.NoError(t, os.WriteFile(gitRelease, nil, 0o600))
	<-listed

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	return resp.GetTerminals()[0]
}

// createTerminalRow stores the row of a terminal and returns its working
// directory, as OpenTerminal does before its startup runs.
func createTerminalRow(ctx context.Context, t *testing.T, svc *Service, id string) string {
	t.Helper()
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: workingDir, HomeDir: workingDir,
		Cols: 80, Rows: 24, Screen: []byte{},
	}))
	return workingDir
}

// startTerminalProcess registers a shell for terminalID in the Manager, as the
// spawn of a startup does. It stops the shell when the test ends.
func startTerminalProcess(ctx context.Context, t *testing.T, svc *Service, terminalID, workingDir string) {
	t.Helper()
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: terminalID, Shell: testutil.TestShell(), WorkingDir: workingDir, Cols: 80, Rows: 24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
}

// A terminal startup ends in this order: the Manager registers the shell, then
// the startup registry drops its entry, then the Worker broadcasts READY. A
// startup that ends while ListTerminals runs git leaves the terminal READY when
// the reply is built.
//
// STARTING was a true answer at the start of the call. The reply must still say
// READY. A browser that opened its watch before the startup ended applied the
// READY broadcast first, and it takes the STARTING reply over it. Nothing sends
// READY again, so the tab keeps STARTING for as long as the page lives:
// handleTerminalInput drops every keystroke that is not sent to a READY tab.
func TestListTerminals_ReportsReadyWhenTheStartupEndsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "terminal-ending-startup"
	workingDir := createTerminalRow(ctx, t, svc, id)
	handle := svc.TerminalStartup.begin(id, func() {})
	require.NotNil(t, handle)

	info := listTerminalsDuringGit(t, d, id, func() {
		// The startup ends now, in the order of runTerminalStartup.
		startTerminalProcess(ctx, t, svc, id, workingDir)
		svc.succeedTerminalStartup(id, handle)
		svc.TerminalStartup.finishEntry(handle)
	})

	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY.String(), info.GetStatus().String(),
		"a terminal whose startup ended during the call is READY in the reply")
	assert.False(t, info.GetExited(), "the shell of that terminal runs")
	assert.Empty(t, info.GetStartupMessage(), "the phase label of the startup must not outlive the startup")
}

// A startup that is still running when git ends is STARTING in the reply. The
// late read must not lose the startup that the registry holds.
func TestListTerminals_ReportsStartingWhenTheStartupStillRunsAfterGit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "terminal-running-startup"
	createTerminalRow(ctx, t, svc, id)
	handle := svc.TerminalStartup.begin(id, func() {})
	require.NotNil(t, handle)
	t.Cleanup(func() { svc.TerminalStartup.abandon(handle) })
	svc.TerminalStartup.setMessage(id, "Starting zsh…")

	info := listTerminalsDuringGit(t, d, id, func() {})

	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING.String(), info.GetStatus().String(),
		"a terminal whose startup still runs after git is STARTING in the reply")
	assert.Equal(t, "Starting zsh…", info.GetStartupMessage())
}

// A startup that fails while ListTerminals runs git leaves the terminal
// STARTUP_FAILED when the reply is built. A browser that applied the failure
// broadcast first takes a STARTING reply over it, and then it shows the spinner
// of a startup that ended, with no error.
func TestListTerminals_ReportsFailedWhenTheStartupFailsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "terminal-failing-startup"
	createTerminalRow(ctx, t, svc, id)
	handle := svc.TerminalStartup.begin(id, func() {})
	require.NotNil(t, handle)

	info := listTerminalsDuringGit(t, d, id, func() {
		svc.failTerminalStartup(id, gitModeResult{}, errors.New("no such shell"), handle)
		svc.TerminalStartup.finishEntry(handle)
	})

	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED.String(), info.GetStatus().String(),
		"a terminal whose startup failed during the call is STARTUP_FAILED in the reply")
	assert.Contains(t, info.GetStartupError(), "no such shell")
}

// The mirror of the first case. A shell that exits while ListTerminals runs git
// leaves the terminal exited when the reply is built. A browser that applied the
// closed event first takes a reply that reads READY and not exited over it, and
// then it sends input to a shell that does not exist.
func TestListTerminals_ReportsExitedWhenTheShellExitsWhileGitRuns(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	const id = "terminal-exiting-shell"
	workingDir := createTerminalRow(ctx, t, svc, id)
	startTerminalProcess(ctx, t, svc, id, workingDir)

	info := listTerminalsDuringGit(t, d, id, func() {
		svc.Terminals.StopTerminal(id)
		svc.Terminals.WaitForExit(id)
	})

	assert.True(t, info.GetExited(), "a shell that exited during the call is exited in the reply")
}
