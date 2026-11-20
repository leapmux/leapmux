package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// skipWithoutPOSIXShell skips a test whose shell is a POSIX script.
func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture uses a POSIX shell")
	}
}

// writeRecordingShell writes a script that plays a shell, and returns its path.
//
// The script first waits half a second for input that arrives before it is
// ready, and prints that input in hex between <EARLY> markers. With
// lineEditor, it then turns bracketed paste on as a line editor does, and
// prints <READY>. Without it, it prints <STARTED> and never turns the mode on.
// After that, it prints each byte that it reads in hex between <B> markers.
// With lineEditor, it turns bracketed paste on again after each byte, as a
// line editor does for each new line, so a second write of the command would
// have every chance to happen. It exits after it reads the byte exitOn, given
// in hex; an empty exitOn never exits.
func writeRecordingShell(t *testing.T, lineEditor bool, exitOn string) string {
	t.Helper()
	ready, again := `printf '<STARTED>'`, ""
	if lineEditor {
		ready, again = `printf '\033[?2004h<READY>'`, `printf '\033[?2004h'`
	}
	shell := filepath.Join(t.TempDir(), "recording-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\n"+
		"stty raw -echo min 0 time 5\n"+
		"early=$(dd bs=256 count=1 2>/dev/null | od -An -tx1 | tr -d ' \\n')\n"+
		"printf '<EARLY>%s</EARLY>' \"$early\"\n"+
		"stty min 1 time 0\n"+
		ready+"\n"+
		"while :; do\n"+
		"  b=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' \\n')\n"+
		"  [ -n \"$b\" ] || exit 0\n"+
		"  printf '<B>%s</B>' \"$b\"\n"+
		"  "+again+"\n"+
		fmt.Sprintf("  [ \"$b\" != %q ] || exit 0\n", exitOn)+
		"done\n")
	return shell
}

// recordedByte matches one byte that the recording shell reports.
var recordedByte = regexp.MustCompile(`<B>([0-9a-f]{2})</B>`)

// recordedInput returns, in hex, the bytes that the recording shell of the
// terminal reported after the last marker, and whether the screen holds the
// marker.
func recordedInput(t *testing.T, svc *Service, terminalID, marker string) (string, bool) {
	t.Helper()
	screen, _, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
	i := strings.LastIndex(string(screen), marker)
	if i < 0 {
		return "", false
	}
	var out strings.Builder
	for _, m := range recordedByte.FindAllStringSubmatch(string(screen[i:]), -1) {
		out.WriteString(m[1])
	}
	return out.String(), true
}

// sendMarkerByte types one byte into the terminal, and waits until the
// recording shell reports it after marker. Every write that reached the PTY
// before the byte shows in front of it.
func sendMarkerByte(t *testing.T, svc *Service, d testRPCDispatcher, terminalID, marker string, b byte) string {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{TerminalId: terminalID, Data: []byte{b}}, w)
	require.Empty(t, w.errors)
	want := hex.EncodeToString([]byte{b})
	var got string
	testutil.RequireEventually(t, func() bool {
		var ok bool
		got, ok = recordedInput(t, svc, terminalID, marker)
		return ok && strings.HasSuffix(got, want)
	}, "the shell reads the byte %q", b)
	return got
}

// openTerminalRunning opens a terminal tab with shell and command through
// OpenTerminal, waits until its PTY exists, and returns its id.
func openTerminalRunning(t *testing.T, svc *Service, d testRPCDispatcher, shell, command string) string {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell: shell, WorkingDir: t.TempDir(), Cols: 200, Rows: 24, InitialCommand: command,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotEmpty(t, resp.GetTerminalId())
	waitForTerminalSpawn(t, svc, resp.GetTerminalId(), "spawn")
	testutil.RegisterTerminalCleanup(t, svc.Terminals, resp.GetTerminalId())
	return resp.GetTerminalId()
}

// setupInitialCommandService builds a service whose terminal timers use a mock clock.
func setupInitialCommandService(t *testing.T) (*Service, testRPCDispatcher, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}), withTerminalClock(clock))
	return svc, d, clock
}

func TestOpenTerminalRunsOneInitialCommandWithADurableClaim(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Parallel()
	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	const marker = "WORKER_INITIAL_COMMAND"
	id := openTerminalRunning(t, svc, d, testutil.TestShell(), "printf '%s\\n' WORKER_INITIAL_''COMMAND")
	testutil.RequireEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
		return strings.Contains(string(screen), marker)
	}, "the original shell should produce the startup receipt")
	sendInteractiveTerminalReceipt(t, svc, d, id, "WORKER_AFTER_INITIAL")
	screen, _, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
	assert.Equal(t, 1, strings.Count(string(screen), marker))
	row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{
		UserID: svc.RegisteredBy().String(), WorkerID: svc.WorkerID, TabID: id,
	})
	require.NoError(t, err)
	assert.True(t, row.InitialCommandClaimed)
}

func TestOpenTerminalRejectsUnsupportedInitialCommandBeforeCreatingATab(t *testing.T) {
	t.Parallel()
	svc, d, writer := setupTestService(t)
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell: "recording-shell", WorkingDir: t.TempDir(), InitialCommand: "printf startup",
	}, writer)
	require.Len(t, writer.errors, 1)
	assert.Empty(t, writer.responses)
	ids, err := svc.Queries.ListAllTerminalIDs(t.Context())
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func sendInteractiveTerminalReceipt(t *testing.T, svc *Service, d testRPCDispatcher, id, marker string) {
	t.Helper()
	command, err := testutil.TestShellOutputCommand(marker)
	require.NoError(t, err)
	writer := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: id, Data: []byte(command + testutil.TestShellEnter()),
	}, writer)
	require.Empty(t, writer.errors)
	testutil.RequireEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
		return strings.Contains(string(screen), marker)
	}, "the interactive shell should produce its receipt")
}

// A quake request that finds the directory's terminal returns it, and its
// shell gets nothing: the command belongs to a shell that the request starts.
func TestOpenTerminal_AQuakeOpenOfTheExistingTerminalRunsNoCommand(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Parallel()
	svc, d, _ := setupInitialCommandService(t)
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	shell := writeRecordingShell(t, true, "")

	openQuake := func(command string) string {
		t.Helper()
		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
			Shell: shell, WorkingDir: dir, Cols: 200, Rows: 24, InitialCommand: command,
			Target: &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.OpenTerminalResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return resp.GetTerminalId()
	}
	first := openQuake("")
	waitForTerminalSpawn(t, svc, first, "spawn")
	testutil.RegisterTerminalCleanup(t, svc.Terminals, first)
	testutil.RequireEventually(t, func() bool {
		_, ready := recordedInput(t, svc, first, "<READY>")
		return ready
	}, "the quake shell is ready")

	require.Equal(t, first, openQuake("make test"), "the second open returns the directory's terminal")
	// The shell turns bracketed paste on again after each byte, so a command
	// that waited for the mode would arrive between the two bytes.
	sendMarkerByte(t, svc, d, first, "<READY>", 'Z')
	assert.Equal(t, "5a59", sendMarkerByte(t, svc, d, first, "<READY>", 'Y'), "the existing shell reads only what the test typed")
}

// A user-requested restart starts an ordinary shell without the earlier command.
func TestRestartTerminal_DoesNotRunTheInitialCommandAgain(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Parallel()
	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	const marker = "WORKER_RESTART_INITIAL"
	id := openTerminalRunning(t, svc, d, testutil.TestShell(), "printf '%s\\n' WORKER_RESTART_''INITIAL; exit")
	waitForTerminalExit(t, svc.Terminals, id)
	screen, _, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
	require.Equal(t, 1, strings.Count(string(screen), marker))

	writer := newTestWriter()
	dispatchRestart(d, id, writer)
	require.Empty(t, writer.errors)
	testutil.RequireEventually(t, func() bool { return svc.Terminals.IsRunning(id) }, "the restarted terminal should have a live shell")
	sendInteractiveTerminalReceipt(t, svc, d, id, "WORKER_RESTART_INTERACTIVE")
	screen, _, _ = svc.Terminals.ScreenSnapshotSince(id, 0)
	assert.Equal(t, 1, strings.Count(string(screen), marker))
}

// An empty initial command runs nothing, and the terminal arms no wait for it.
func TestOpenTerminal_AnEmptyInitialCommandRunsNothing(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Parallel()
	svc, d, clock := setupInitialCommandService(t)
	defer drainAllInFlight(svc)

	id := openTerminalRunning(t, svc, d, writeRecordingShell(t, true, ""), "")
	testutil.RequireEventually(t, func() bool {
		_, ready := recordedInput(t, svc, id, "<READY>")
		return ready
	}, "the shell is ready")
	_, armed := clock.Peek()
	assert.False(t, armed, "an ordinary launch does not arm handoff timers")
	assert.Equal(t, "5a", sendMarkerByte(t, svc, d, id, "<READY>", 'Z'))
}

// ListTerminals reports the exit code of a shell that ended: from the
// manager while it holds the terminal, and from the row that the exit handler
// persisted after a Worker restart, when only the row remains. A running
// shell reports none.
func TestListTerminals_ReportsTheExitCodeOfAShellThatEnded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cmd.exe under ConPTY does not pass `exit N` to its exit code; see TestRestartTerminal_HappyPath")
	}
	t.Parallel()
	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	id := openTerminalViaRPC(t, svc, d, w, t.TempDir())
	running := listOneTerminal(t, d, id)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, running.GetStatus())
	assert.Nil(t, running.ExitCode, "a running shell has no exit code")

	exitTerminalAndWait(t, svc, d, id, " 7")
	exited := listOneTerminal(t, d, id)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, exited.GetStatus())
	require.NotNil(t, exited.ExitCode)
	assert.Equal(t, int32(7), exited.GetExitCode())

	// A Worker restart leaves only the row, which the exit handler wrote.
	svc.Terminals.RemoveTerminal(id)
	require.False(t, svc.Terminals.HasTerminal(id))
	persisted := listOneTerminal(t, d, id)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, persisted.GetStatus())
	require.NotNil(t, persisted.ExitCode)
	assert.Equal(t, int32(7), persisted.GetExitCode())
}

// listOneTerminal lists the terminal id through ListTerminals and returns its
// record.
func listOneTerminal(t *testing.T, d testRPCDispatcher, id string) *leapmuxv1.TerminalInfo {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: []string{id}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	return resp.GetTerminals()[0]
}

// The creation's runtime carries the initial command, so a creation that a
// Worker restart resumes starts its shell with the command of the request
// that opened the tab.
func TestCreationRestart_KeepsTheInitialCommand(t *testing.T) {
	t.Parallel()
	for _, state := range []int64{creationPending, creationPublished} {
		t.Run(leapmuxv1.TabCreationState(state).String(), func(t *testing.T) {
			t.Parallel()
			hub := newRecoveryPublicationHub(t)
			directory := t.TempDir()
			path := filepath.Join(directory, "worker.db")
			oldDatabase := openRecoveryDatabase(t, path)
			oldService, _ := newRecoveryService(t, hub.config(oldDatabase, directory))
			defer oldService.Shutdown()
			intent := hub.intent("terminal-with-command")
			_, request := recoveryCreationRequest(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, intent, directory)
			runtime := &terminalCreationRuntime{
				Startup: creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}},
				Options: terminal.Options{ID: intent.TabId, WorkingDir: directory, Shell: testutil.TestShell(), Cols: 80, Rows: 25, InitialCommand: "make test\nmake lint"},
				Title:   "Saved terminal",
			}
			seedRecoveryCreation(t, oldService, recoveryCreationSeed{request: request, publication: &leapmuxv1.TabPublication{Creation: intent, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}, runtime: runtime, state: state,
				write: func(ctx context.Context, q *db.Queries, _ *sql.Tx) error {
					return q.CreateTerminal(ctx, db.CreateTerminalParams{ID: intent.TabId, WorkingDir: directory, Shell: runtime.Options.Shell, Title: runtime.Title, Cols: 80, Rows: 25, Screen: []byte{}})
				},
			})
			oldService.Shutdown()
			require.NoError(t, oldDatabase.Close())
			freshDatabase := openRecoveryDatabase(t, path)
			fresh, _ := newRecoveryService(t, hub.config(freshDatabase, directory))
			defer fresh.Shutdown()
			captured := make(chan terminal.Options, 1)
			fresh.startTerminalFn = func(_ context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
				select {
				case captured <- opts:
				default:
				}
				return errors.New("the test captures recovered terminal startup")
			}
			ctx := creationOutcomeContext(t)
			require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
			var opts terminal.Options
			select {
			case opts = <-captured:
			case <-ctx.Done():
				t.Fatal("recovery did not start the terminal")
			}
			fresh.TerminalStartup.WaitForInFlight()
			assert.Equal(t, "make test\nmake lint", opts.InitialCommand)
		})
	}
}
