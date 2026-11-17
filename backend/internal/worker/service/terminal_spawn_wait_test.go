package service

import (
	"bytes"
	"context"
	"fmt"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// terminalSpawnReportReadLimit is the longest time that terminalSpawnReport
// waits for the row of the terminal. The database of a test service holds one
// connection, so a startup that stalls while it holds that connection would
// stall an unlimited read too, and the report would never print.
const terminalSpawnReportReadLimit = 2 * time.Second

// waitForTerminalSpawn waits until the terminal manager holds the PTY of
// terminalID, as testutil.AssertEventually does. A wait that times out logs
// why the PTY is absent (see terminalSpawnReport): a spawn that failed leaves
// its error in the row or in the startup registry, and a startup that stalls
// shows where in the goroutine stacks.
func waitForTerminalSpawn(t *testing.T, svc *Service, terminalID string, msgAndArgs ...any) bool {
	t.Helper()
	if testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, msgAndArgs...) {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalSpawnReportReadLimit)
	defer cancel()
	t.Log(terminalSpawnReport(ctx, svc, terminalID))
	return false
}

// terminalSpawnReport states why the terminal manager holds no PTY for
// terminalID: the startup registry entry of the terminal, the startup
// goroutines of the terminal that still run, the terminal row, and the stack of
// every goroutine.
//
// It waits for nothing that a stalled startup can hold. It records the stacks
// first. It only tries the lock of the registry. It reads the row under ctx,
// which the caller limits.
func terminalSpawnReport(ctx context.Context, svc *Service, terminalID string) string {
	var stacks bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
		fmt.Fprintf(&stacks, "the goroutine stacks are unreadable: %v\n", err)
	}
	var report strings.Builder
	fmt.Fprintf(&report, "The terminal manager holds no PTY for the terminal %s.\n", terminalID)
	report.WriteString(startupRegistryReport(&svc.TerminalStartup.startupCore, terminalID))
	if row, err := svc.Queries.GetTerminal(ctx, terminalID); err != nil {
		fmt.Fprintf(&report, "row: unreadable: %v\n", err)
	} else {
		fmt.Fprintf(&report, "row: startup_error=%q closed=%t workspace_archived=%t\n", row.StartupError, row.ClosedAt.Valid, row.WorkspaceArchived)
	}
	report.WriteString("goroutines:\n")
	report.Write(stacks.Bytes())
	return report.String()
}

// startupRegistryReport states the entry of id in r, and how many startup
// goroutines of id still run. It takes the lock of r with TryLock, because a
// stalled startup can hold that lock, and a report that waited for it would
// never print.
func startupRegistryReport(r *startupCore, id string) string {
	if !r.mu.TryLock() {
		return "registry: another goroutine holds the lock of the registry, so its state is unreadable\n"
	}
	defer r.mu.Unlock()
	var report strings.Builder
	if entry := r.entries[id]; entry == nil {
		report.WriteString("registry: no entry\n")
	} else {
		fmt.Fprintf(&report, "registry: entry failed=%t error=%q message=%q handed_off=%t\n",
			entry.failed, entry.startupError, entry.startupMessage, entry.creationHandedOff)
	}
	running := 0
	for entry := r.inflight[id]; entry != nil; entry = entry.enclosing {
		running++
	}
	fmt.Fprintf(&report, "registry: %d startup goroutine(s) of the terminal still run\n", running)
	return report.String()
}

func TestTerminalSpawnReport_StatesTheRowAndTheStacks(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         "failed-spawn",
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
		Screen:     []byte{},
	}))
	require.NoError(t, svc.Queries.SetTerminalStartupError(context.Background(), db.SetTerminalStartupErrorParams{
		ID:           "failed-spawn",
		StartupError: "start pty: no such device",
	}))

	report := terminalSpawnReport(context.Background(), svc, "failed-spawn")

	assert.Contains(t, report, "The terminal manager holds no PTY for the terminal failed-spawn.")
	assert.Contains(t, report, "registry: no entry\n")
	assert.Contains(t, report, "registry: 0 startup goroutine(s) of the terminal still run\n")
	assert.Contains(t, report, `row: startup_error="start pty: no such device" closed=false workspace_archived=false`)
	// The stack of this test's own goroutine, which wrote the report.
	assert.Contains(t, report, "goroutines:\n")
	assert.Contains(t, report, "TestTerminalSpawnReport_StatesTheRowAndTheStacks")
}

func TestTerminalSpawnReport_StatesARunningStartup(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	_, handle := svc.beginTerminalStartup("starting-spawn", testutil.TestShell())
	require.NotNil(t, handle)
	t.Cleanup(func() { svc.TerminalStartup.abandon(handle) })

	report := startupRegistryReport(&svc.TerminalStartup.startupCore, "starting-spawn")

	assert.Contains(t, report, "registry: entry failed=false")
	assert.Contains(t, report, "handed_off=false")
	assert.Contains(t, report, "registry: 1 startup goroutine(s) of the terminal still run\n")
}

func TestTerminalSpawnReport_StatesAFailedStartup(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	_, handle := svc.beginTerminalStartup("failed-startup", testutil.TestShell())
	require.NotNil(t, handle)
	svc.TerminalStartup.fail(handle, "start pty: no such device")
	svc.TerminalStartup.finishEntry(handle)

	report := startupRegistryReport(&svc.TerminalStartup.startupCore, "failed-startup")

	assert.Equal(t, "registry: entry failed=true error=\"start pty: no such device\" message=\"\" handed_off=false\n"+
		"registry: 0 startup goroutine(s) of the terminal still run\n", report)
}

func TestTerminalSpawnReport_NamesAHeldRegistryLock(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	registry := &svc.TerminalStartup.startupCore
	registry.mu.Lock()
	report := startupRegistryReport(registry, "any")
	registry.mu.Unlock()

	assert.Equal(t, "registry: another goroutine holds the lock of the registry, so its state is unreadable\n", report)
}

func TestTerminalSpawnReport_GivesUpOnAHeldDatabaseConnection(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	// The database of a test service holds one connection, and this transaction
	// takes it, as a stalled startup inside a transaction would.
	tx, err := svc.DB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := terminalSpawnReport(ctx, svc, "any")

	assert.Contains(t, report, "row: unreadable: context canceled")
	assert.Contains(t, report, "goroutines:\n")
}
