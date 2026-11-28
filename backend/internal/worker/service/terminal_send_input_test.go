package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// runShellCommand sends line to a live terminal the way `terminal run` does:
// the line as a paste, then the Enter key, in one request. It waits until the
// shell printed marker, which the line prints.
func runShellCommand(t *testing.T, d testRPCDispatcher, terminalID, marker string) *testResponseWriter {
	t.Helper()
	line, err := testutil.TestShellOutputCommand(marker)
	require.NoError(t, err)
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte(line),
		Paste:      true,
		Keys:       []string{"Enter"},
	}, w)
	return w
}

// screenContains reports whether the retained screen of a terminal holds
// needle.
func screenContains(t *testing.T, svc *Service, terminalID, needle string) bool {
	t.Helper()
	screen, _, _, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
	return bytes.Contains(screen, []byte(needle))
}

// The handler writes the text and then the keys that the request lists, so a
// command with the Enter key runs without a newline in its text.
func TestSendInput_PressesTheKeysAfterTheText(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-keys")

	w := runShellCommand(t, d, "term-keys", "keys_after_text")
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-keys", "keys_after_text")
	}, "the Enter key after the text runs the command")
}

// An unknown key name refuses the whole request before the handler writes
// anything: the text of the refused request never reaches the shell.
func TestSendInput_AnUnknownKeyWritesNothing(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-refuse")

	for name, keys := range map[string][]string{
		"an unknown name":         {"enter", "entr"},
		"a modifier with no code": {"ctrl-enter"},
		"an empty name":           {""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
				TerminalId: "term-refuse",
				Data:       []byte("refused_input"),
				Keys:       keys,
			}, w)
			require.Len(t, w.errors, 1)
			assert.EqualValues(t, codes.InvalidArgument, w.errors[0].code)
			assert.Empty(t, w.responses)
		})
	}

	// The shell echoes its input in order, so a refused text that reached it
	// would show before the output of the command that follows.
	w := runShellCommand(t, d, "term-refuse", "after_refusals")
	require.Empty(t, w.errors)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-refuse", "after_refusals")
	}, "the command after the refusals runs")
	assert.False(t, screenContains(t, svc, "term-refuse", "refused_input"), "a refused request wrote its text")
}

// The handler sends the data of a paste request as a paste: the Worker encodes
// it for the bracketed paste that the program in the terminal enabled, and
// presses the keys after it, in one write. The shell of the terminal is a
// script that turns bracketed paste on, reads the raw bytes that reach the
// PTY, and prints them in hex.
func TestSendInput_EncodesAPasteForTheModesOfTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recording shell is a POSIX script that puts the terminal in raw mode with stty")
	}
	svc, d, _ := setupTestService(t)
	const want = "\x1b[200~a\rb\x1b[201~\r"
	shell := filepath.Join(t.TempDir(), "recording-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\n"+
		"stty raw -echo\n"+
		"printf '\\033[?2004h<READY>\\n'\n"+
		fmt.Sprintf("printf '<GOT>%%s</GOT>\\n' \"$(dd bs=1 count=%d 2>/dev/null | od -An -tx1 | tr -d ' \\n')\"\n", len(want))+
		"exec cat >/dev/null\n")
	workingDir := t.TempDir()
	require.NoError(t, svc.Terminals.StartTerminal(t.Context(), terminal.Options{
		ID: "term-paste", Shell: shell, WorkingDir: workingDir, Cols: 80, Rows: 24,
	}, func(terminal.Output) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, "term-paste")
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: "term-paste", WorkingDir: workingDir, Cols: 80, Rows: 24, Screen: []byte{},
	}))
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-paste", "<READY>")
	}, "the shell turns bracketed paste on and waits for input")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: "term-paste",
		Data:       []byte("a\nb"),
		Paste:      true,
		Keys:       []string{"enter"},
	}, w)
	require.Empty(t, w.errors)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, "term-paste", "<GOT>"+hex.EncodeToString([]byte(want))+"</GOT>")
	}, "the paste arrives between the markers, with CR for its line ending, and Enter follows it")
}

// The refusal identifies the key, so the caller knows which one to correct.
func TestSendInput_TheRefusalIdentifiesTheKey(t *testing.T) {
	svc, d, _ := setupTestService(t)
	startTestTerminal(t, svc, t.Context(), "term-name")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: "term-name",
		Keys:       []string{"up", "pgup"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, `"pgup"`)
}

// observeStartupWait installs a hook that signals once WaitForStartup is about
// to block on terminalID's in-flight startup entry.
func observeStartupWait(svc *Service, terminalID string) <-chan struct{} {
	entered := make(chan struct{})
	var once sync.Once
	svc.TerminalStartup.mu.Lock()
	svc.TerminalStartup.onWaitEnter = func(id string) {
		if id == terminalID {
			once.Do(func() { close(entered) })
		}
	}
	svc.TerminalStartup.mu.Unlock()
	return entered
}

// observeDuplicateSendWait installs a hook that signals once execute is about
// to block waiting on an in-flight duplicate sendInputReceipts entry.
func observeDuplicateSendWait(svc *Service) <-chan struct{} {
	entered := make(chan struct{})
	var once sync.Once
	svc.sendInputReceipts.mu.Lock()
	svc.sendInputReceipts.onDuplicateWait = func() {
		once.Do(func() { close(entered) })
	}
	svc.sendInputReceipts.mu.Unlock()
	return entered
}

func openPausedTerminal(t *testing.T, svc *Service, d testRPCDispatcher, startFn func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error) (terminalID string, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	proceedCh := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	release = func() {
		releaseOnce.Do(func() { close(proceedCh) })
	}
	t.Cleanup(release)
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		enteredOnce.Do(func() { close(enteredCh) })
		select {
		case <-proceedCh:
		case <-ctx.Done():
			return ctx.Err()
		}
		return startFn(ctx, opts, outFn, exitFn)
	}

	wOpen := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
		Cols:       80,
		Rows:       24,
	}, wOpen)
	require.Empty(t, wOpen.errors)
	require.Len(t, wOpen.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(wOpen.responses[0].GetPayload(), &openResp))
	id := openResp.GetTerminalId()
	require.NotEmpty(t, id)

	ctx := testutil.DeadlineContext(t)
	select {
	case <-enteredCh:
	case <-ctx.Done():
		t.Fatal("startTerminalFn was never invoked")
	}
	return id, release
}

// SendInput blocks while PTY startup is in flight and delivers input once
// startup transitions to READY.
func TestSendInput_WaitsForInFlightStartupAndDeliversInput(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	terminalID, release := openPausedTerminal(t, svc, d, svc.Terminals.StartTerminal)
	defer release()
	t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })
	require.False(t, svc.Terminals.HasTerminal(terminalID), "precondition: PTY is not registered yet")

	line, err := testutil.TestShellOutputCommand("delivered_after_startup")
	require.NoError(t, err)

	enteredWait := observeStartupWait(svc, terminalID)
	wSend := newTestWriter()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
			TerminalId: terminalID,
			Data:       []byte(line),
			Paste:      true,
			Keys:       []string{"Enter"},
			RequestId:  "req-after-startup",
		}, wSend)
	}()

	select {
	case <-enteredWait:
	case <-ctx.Done():
		t.Fatal("SendInput did not enter WaitForStartup")
	}
	select {
	case <-sendDone:
		t.Fatal("SendInput returned before startup finished")
	default:
	}

	release()

	select {
	case <-sendDone:
	case <-ctx.Done():
		t.Fatal("SendInput did not complete after startup succeeded")
	}
	require.Empty(t, wSend.errors)
	require.Len(t, wSend.responses, 1)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, terminalID, "delivered_after_startup")
	}, "input delivered after startup reached READY")
}

// SendInput fails cleanly with FailedPrecondition both when an in-flight
// startup transitions to STARTUP_FAILED and when startup has already failed.
func TestSendInput_FailsCleanlyWhenStartupFails(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	terminalID, release := openPausedTerminal(t, svc, d, func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("pty spawn failed")
	})
	defer release()

	enteredWait := observeStartupWait(svc, terminalID)
	wWait := newTestWriter()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
			TerminalId: terminalID,
			Data:       []byte("echo nope"),
			Keys:       []string{"Enter"},
			RequestId:  "req-failed-startup",
		}, wWait)
	}()

	select {
	case <-enteredWait:
	case <-ctx.Done():
		t.Fatal("SendInput did not enter WaitForStartup")
	}
	release()

	select {
	case <-sendDone:
	case <-ctx.Done():
		t.Fatal("SendInput did not wake after startup failed")
	}
	require.Len(t, wWait.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wWait.errors[0].code)
	assert.Contains(t, wWait.errors[0].message, "pty spawn failed")
	assert.Empty(t, wWait.responses)

	// A subsequent SendInput after STARTUP_FAILED also fails immediately without blocking.
	wAfter := newTestWriter()
	dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte("echo still-failed"),
		Keys:       []string{"Enter"},
	}, wAfter)
	require.Len(t, wAfter.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wAfter.errors[0].code)
	assert.Contains(t, wAfter.errors[0].message, "pty spawn failed")
	assert.Empty(t, wAfter.responses)
}

// manualDeadlineContext triggers context.DeadlineExceeded deterministically
// when expire is called, avoiding wall-clock sleeps or short timeouts.
type manualDeadlineContext struct {
	context.Context
	done     chan struct{}
	once     sync.Once
	fired    atomic.Bool
	stopWait func() bool
}

func newManualDeadlineContext(parent context.Context) (*manualDeadlineContext, func()) {
	c := &manualDeadlineContext{
		Context: parent,
		done:    make(chan struct{}),
	}
	c.stopWait = context.AfterFunc(parent, func() {
		c.once.Do(func() { close(c.done) })
	})
	expire := func() {
		c.stopWait()
		c.once.Do(func() {
			c.fired.Store(true)
			close(c.done)
		})
	}
	return c, expire
}

func (c *manualDeadlineContext) Done() <-chan struct{} {
	return c.done
}

func (c *manualDeadlineContext) Err() error {
	if c.fired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

func TestManualDeadlineContext_ParentCancellationClosesDone(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		mc, expire := newManualDeadlineContext(parent)
		defer expire()

		cancel()
		<-mc.Done()
		assert.ErrorIs(t, mc.Err(), context.Canceled)
		expire()
		assert.ErrorIs(t, mc.Err(), context.Canceled)
	})
}

// SendInput honors ctx cancellation and deadline expiration during startup wait
// without reserving a sendInputReceipts entry, so a retry with the same
// request_id succeeds once startup finishes.
func TestSendInput_ContextEndDuringStartupWaitAllowsRetryWithSameRequestID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		marker   string
		reqID    string
		wantCode codes.Code
		makeCtx  func(t *testing.T, parent context.Context) (context.Context, func())
	}{
		{
			name:     "canceled",
			marker:   "retried_after_cancel",
			reqID:    "req-cancel-then-retry",
			wantCode: codes.Canceled,
			makeCtx: func(_ *testing.T, parent context.Context) (context.Context, func()) {
				return context.WithCancel(parent)
			},
		},
		{
			name:     "deadline exceeded",
			marker:   "retried_after_deadline",
			reqID:    "req-deadline-then-retry",
			wantCode: codes.DeadlineExceeded,
			makeCtx: func(t *testing.T, parent context.Context) (context.Context, func()) {
				deadlineCtx, expire := newManualDeadlineContext(parent)
				t.Cleanup(expire)
				return deadlineCtx, expire
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			terminalID, release := openPausedTerminal(t, svc, d, svc.Terminals.StartTerminal)
			defer release()
			t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })

			line, err := testutil.TestShellOutputCommand(tc.marker)
			require.NoError(t, err)
			req := &leapmuxv1.SendInputRequest{
				TerminalId: terminalID,
				Data:       []byte(line),
				Paste:      true,
				Keys:       []string{"Enter"},
				RequestId:  tc.reqID,
			}

			endCtx, triggerEnd := tc.makeCtx(t, ctx)
			enteredWait := observeStartupWait(svc, terminalID)
			wEnded := newTestWriter()
			done := make(chan struct{})
			go func() {
				defer close(done)
				dispatchWithContext(d, endCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wEnded)
			}()
			select {
			case <-enteredWait:
			case <-ctx.Done():
				t.Fatal("SendInput did not enter WaitForStartup")
			}
			triggerEnd()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("SendInput did not exit after ctx ended")
			}
			require.Len(t, wEnded.errors, 1)
			assert.Equal(t, int32(tc.wantCode), wEnded.errors[0].code)
			assert.Empty(t, wEnded.responses)

			// Confirm no receipt entry was reserved for the interrupted attempt.
			svc.sendInputReceipts.mu.Lock()
			_, reserved := svc.sendInputReceipts.entries[terminalID+"\x00"+tc.reqID]
			svc.sendInputReceipts.mu.Unlock()
			assert.False(t, reserved, "interrupted startup wait must not reserve a sendInputReceipts entry")

			// Unblock startup and retry with the exact same request_id.
			release()
			wRetry := newTestWriter()
			dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wRetry)
			require.Empty(t, wRetry.errors)
			require.Len(t, wRetry.responses, 1)
			testutil.AssertEventually(t, func() bool {
				return screenContains(t, svc, terminalID, tc.marker)
			}, "retry with same request_id delivered input once startup reached READY")
		})
	}
}

func TestSendInput_DBDetectedInterruptionDuringStartupReturnsFailedPrecondition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		wantMsg string
		mutate  func(t *testing.T, ctx context.Context, svc *Service, terminalID string)
	}{
		{
			name:    "closed in DB",
			wantMsg: "closed during startup",
			mutate: func(t *testing.T, ctx context.Context, svc *Service, terminalID string) {
				_, err := svc.Queries.CloseTerminal(ctx, terminalID)
				require.NoError(t, err)
			},
		},
		{
			name:    "archived in DB",
			wantMsg: "archived during startup",
			mutate: func(t *testing.T, ctx context.Context, svc *Service, terminalID string) {
				_, err := svc.Queries.SetTerminalWorkspaceArchived(ctx, db.SetTerminalWorkspaceArchivedParams{
					ID:                terminalID,
					WorkspaceArchived: true,
				})
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			terminalID, release := openPausedTerminal(t, svc, d, svc.Terminals.StartTerminal)
			defer release()
			t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })

			enteredWait := observeStartupWait(svc, terminalID)
			wSend := newTestWriter()
			sendDone := make(chan struct{})
			go func() {
				defer close(sendDone)
				dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
					TerminalId: terminalID,
					Data:       []byte("echo interrupted"),
					Keys:       []string{"Enter"},
				}, wSend)
			}()

			select {
			case <-enteredWait:
			case <-ctx.Done():
				t.Fatal("SendInput did not enter WaitForStartup")
			}

			tc.mutate(t, ctx, svc, terminalID)
			release()

			select {
			case <-sendDone:
			case <-ctx.Done():
				t.Fatal("SendInput did not wake after DB-detected interruption")
			}
			require.Len(t, wSend.errors, 1)
			assert.Equal(t, int32(codes.FailedPrecondition), wSend.errors[0].code)
			assert.Contains(t, wSend.errors[0].message, tc.wantMsg)
			assert.Empty(t, wSend.responses)
		})
	}
}

func TestSendInput_NonRunningTerminalReturnsFailedPrecondition(t *testing.T) {
	t.Parallel()

	t.Run("persisted startup error without registry entry", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		const id = "term-persisted-fail"
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkingDir: t.TempDir(), Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
		}))
		require.NoError(t, svc.Queries.SetTerminalStartupError(ctx, db.SetTerminalStartupErrorParams{
			ID:           id,
			StartupError: "shell binary missing",
		}))

		w := newTestWriter()
		dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
			TerminalId: id,
			Data:       []byte("echo hi"),
		}, w)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal startup failed: shell binary missing")
		assert.Empty(t, w.responses)
	})

	t.Run("exited terminal not held in manager", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		const id = "term-db-only-exited"
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkingDir: t.TempDir(), Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
		}))

		w := newTestWriter()
		dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
			TerminalId: id,
			Data:       []byte("echo hi"),
		}, w)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal is not running")
		assert.Empty(t, w.responses)
	})

	t.Run("exited terminal held in manager", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		svc, d, _ := setupTestService(t)
		defer drainAllInFlight(svc)

		const id = "term-held-exited"
		dir := t.TempDir()
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
		}))
		require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
			ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 24,
		}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
		testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
		exitTerminalAndWait(t, svc, d, id, " 0")

		w := newTestWriter()
		dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
			TerminalId: id,
			Data:       []byte("echo hi"),
		}, w)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal is not running")
		assert.Empty(t, w.responses)
	})
}

func TestSendInput_DuplicateInFlightContextEndSendsErrorFrame(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		wantCode codes.Code
		makeCtx  func(t *testing.T, parent context.Context) (context.Context, func())
	}{
		{
			name:     "canceled",
			wantCode: codes.Canceled,
			makeCtx: func(_ *testing.T, parent context.Context) (context.Context, func()) {
				return context.WithCancel(parent)
			},
		},
		{
			name:     "deadline exceeded",
			wantCode: codes.DeadlineExceeded,
			makeCtx: func(t *testing.T, parent context.Context) (context.Context, func()) {
				deadlineCtx, expire := newManualDeadlineContext(parent)
				t.Cleanup(expire)
				return deadlineCtx, expire
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			const terminalID = "term-dup-inflight"
			startTestTerminal(t, svc, ctx, terminalID)

			req := &leapmuxv1.SendInputRequest{
				TerminalId: terminalID,
				Data:       []byte("echo dup"),
				Keys:       []string{"Enter"},
				RequestId:  "req-dup-inflight",
			}
			fp := terminalSendInputFingerprint(req)
			key := terminalID + "\x00" + req.GetRequestId()
			inflightDone := make(chan struct{})
			svc.sendInputReceipts.mu.Lock()
			if svc.sendInputReceipts.entries == nil {
				svc.sendInputReceipts.entries = make(map[string]*sendInputReceiptEntry)
			}
			svc.sendInputReceipts.entries[key] = &sendInputReceiptEntry{key: key, fp: fp, done: inflightDone}
			svc.sendInputReceipts.mu.Unlock()
			defer close(inflightDone)

			endCtx, triggerEnd := tc.makeCtx(t, ctx)
			enteredWait := observeDuplicateSendWait(svc)
			w := newTestWriter()
			done := make(chan struct{})
			go func() {
				defer close(done)
				dispatchWithContext(d, endCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, w)
			}()

			select {
			case <-enteredWait:
			case <-ctx.Done():
				t.Fatal("duplicate SendInput did not enter wait")
			}
			triggerEnd()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("duplicate SendInput did not exit after ctx ended")
			}
			require.Len(t, w.errors, 1)
			assert.Equal(t, int32(tc.wantCode), w.errors[0].code)
			assert.Empty(t, w.responses)
		})
	}
}

func TestSendInput_CompletedReceiptBypassesSubsequentStartupState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		setupStartup func(t *testing.T, svc *Service, terminalID string) func()
	}{
		{
			name: "in-flight restart startup",
			setupStartup: func(t *testing.T, svc *Service, terminalID string) func() {
				h := svc.TerminalStartup.begin(terminalID, func() {})
				require.NotNil(t, h)
				return func() { svc.TerminalStartup.abandon(h) }
			},
		},
		{
			name: "failed restart startup",
			setupStartup: func(t *testing.T, svc *Service, terminalID string) func() {
				h := svc.TerminalStartup.begin(terminalID, func() {})
				require.NotNil(t, h)
				svc.TerminalStartup.fail(h, "restart boom")
				svc.TerminalStartup.finishEntry(h)
				return func() { svc.TerminalStartup.cancelAndClear(terminalID, keepWorktreeOnClose) }
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			const terminalID = "term-completed-receipt"
			startTestTerminal(t, svc, ctx, terminalID)

			req := &leapmuxv1.SendInputRequest{
				TerminalId: terminalID,
				Data:       []byte("echo once"),
				Keys:       []string{"Enter"},
				RequestId:  "req-completed-1",
			}
			wFirst := newTestWriter()
			dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wFirst)
			require.Empty(t, wFirst.errors)
			require.Len(t, wFirst.responses, 1)

			cleanupStartup := tc.setupStartup(t, svc, terminalID)
			defer cleanupStartup()

			// Same payload and request_id succeeds immediately without waiting on startup.
			wDup := newTestWriter()
			dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wDup)
			require.Empty(t, wDup.errors)
			require.Len(t, wDup.responses, 1)

			// Different payload with the same request_id fails immediately with conflict.
			wConflict := newTestWriter()
			dispatchWithContext(d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
				TerminalId: terminalID,
				Data:       []byte("echo different"),
				Keys:       []string{"Enter"},
				RequestId:  "req-completed-1",
			}, wConflict)
			require.Len(t, wConflict.errors, 1)
			assert.Equal(t, int32(codes.FailedPrecondition), wConflict.errors[0].code)
			assert.Contains(t, wConflict.errors[0].message, "different terminal input payload")
			assert.Empty(t, wConflict.responses)
		})
	}
}

func TestTerminalStartup_WaitForStartupOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("no active entry returns nil immediately", func(t *testing.T) {
		t.Parallel()
		reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
		require.NoError(t, reg.WaitForStartup(t.Context(), "t-none"))
	})

	t.Run("no active entry during shutdown returns nil", func(t *testing.T) {
		t.Parallel()
		reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
		reg.closeClaims()
		require.NoError(t, reg.WaitForStartup(t.Context(), "t-running"))
	})

	t.Run("succeed unblocks waiter with nil", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			h := beginForTest(t, &reg.startupCore, "t-ok")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-ok") }()
			synctest.Wait()
			reg.succeed("t-ok", h)
			require.NoError(t, <-errCh)
		})
	})

	t.Run("fail unblocks waiter with startup failed error", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			h := beginForTest(t, &reg.startupCore, "t-fail")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-fail") }()
			synctest.Wait()
			reg.fail(h, "shell missing")
			err := <-errCh
			require.ErrorIs(t, err, errStartupFailed)
			assert.Equal(t, "terminal startup failed: shell missing", err.Error())
		})
	})

	t.Run("fail reports startup failed even if entry is evicted before waiter wakes", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			reg := newTerminalStartupRegistry(clock)
			h := beginForTest(t, &reg.startupCore, "t-evict")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-evict") }()
			synctest.Wait()
			reg.fail(h, "evicted failure")
			clock.Advance(failedEntryTTL)
			err := <-errCh
			require.ErrorIs(t, err, errStartupFailed)
			assert.Contains(t, err.Error(), "evicted failure")
		})
	})

	t.Run("fail reports startup failed even if a concurrent restart replaces the entry", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			h := beginForTest(t, &reg.startupCore, "t-replaced")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-replaced") }()
			synctest.Wait()
			reg.fail(h, "initial failure")
			replacement := beginForTest(t, &reg.startupCore, "t-replaced")
			defer reg.succeed("t-replaced", replacement)
			err := <-errCh
			require.ErrorIs(t, err, errStartupFailed)
			assert.Contains(t, err.Error(), "initial failure")
		})
	})

	t.Run("close interrupts waiter", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			beginForTest(t, &reg.startupCore, "t-close")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-close") }()
			synctest.Wait()
			reg.cancelAndClear("t-close", keepWorktreeOnClose)
			err := <-errCh
			require.ErrorIs(t, err, errStartupWaitClosed)
			assert.Equal(t, "terminal closed during startup", err.Error())
		})
	})

	t.Run("archive interrupts waiter", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			beginForTest(t, &reg.startupCore, "t-archive")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-archive") }()
			synctest.Wait()
			reg.cancelForArchive("t-archive")
			err := <-errCh
			require.ErrorIs(t, err, errStartupWaitArchived)
			assert.Equal(t, "terminal archived during startup", err.Error())
		})
	})

	t.Run("retireInterrupted stamps closed and archived for waiters", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))

			hClosed := beginForTest(t, &reg.startupCore, "t-db-close")
			closedCh := make(chan error, 1)
			go func() { closedCh <- reg.WaitForStartup(context.Background(), "t-db-close") }()
			synctest.Wait()
			reg.retireInterrupted("t-db-close", hClosed, true, false)
			err := <-closedCh
			require.ErrorIs(t, err, errStartupWaitClosed)
			assert.Equal(t, "terminal closed during startup", err.Error())

			hArchived := beginForTest(t, &reg.startupCore, "t-db-archive")
			archivedCh := make(chan error, 1)
			go func() { archivedCh <- reg.WaitForStartup(context.Background(), "t-db-archive") }()
			synctest.Wait()
			reg.retireInterrupted("t-db-archive", hArchived, false, true)
			err = <-archivedCh
			require.ErrorIs(t, err, errStartupWaitArchived)
			assert.Equal(t, "terminal archived during startup", err.Error())
		})
	})

	t.Run("retireInterrupted keeps close precedence over archive", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))

			hBoth := beginForTest(t, &reg.startupCore, "t-db-both")
			bothCh := make(chan error, 1)
			go func() { bothCh <- reg.WaitForStartup(context.Background(), "t-db-both") }()
			synctest.Wait()
			reg.retireInterrupted("t-db-both", hBoth, true, true)
			err := <-bothCh
			require.ErrorIs(t, err, errStartupWaitClosed)
			assert.NotErrorIs(t, err, errStartupWaitArchived)
			assert.False(t, reg.archiveStopped(hBoth))

			hRaced := beginForTest(t, &reg.startupCore, "t-db-close-raced")
			racedCh := make(chan error, 1)
			go func() { racedCh <- reg.WaitForStartup(context.Background(), "t-db-close-raced") }()
			synctest.Wait()
			reg.mu.Lock()
			hRaced.closeRaced = true
			reg.mu.Unlock()
			reg.retireInterrupted("t-db-close-raced", hRaced, false, true)
			err = <-racedCh
			require.ErrorIs(t, err, errStartupWaitClosed)
			assert.NotErrorIs(t, err, errStartupWaitArchived)
			assert.False(t, reg.archiveStopped(hRaced))
		})
	})

	t.Run("retireInterrupted ignores stale handle after replacement claim", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))

			staleH := beginForTest(t, &reg.startupCore, "t-stale-retire")
			reg.succeed("t-stale-retire", staleH)

			replacement := beginForTest(t, &reg.startupCore, "t-stale-retire")
			waitCh := make(chan error, 1)
			go func() { waitCh <- reg.WaitForStartup(context.Background(), "t-stale-retire") }()
			synctest.Wait()

			reg.retireInterrupted("t-stale-retire", staleH, true, false)
			synctest.Wait()

			reg.mu.Lock()
			current := reg.entries["t-stale-retire"]
			reg.mu.Unlock()
			assert.Same(t, replacement, current, "stale handle must not delete the replacement entry")
			select {
			case err := <-waitCh:
				t.Fatalf("stale retireInterrupted released the replacement waiter with %v", err)
			default:
			}

			reg.succeed("t-stale-retire", replacement)
			require.NoError(t, <-waitCh)
		})
	})

	t.Run("shutdown interrupts waiter", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			beginForTest(t, &reg.startupCore, "t-shutdown")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-shutdown") }()
			synctest.Wait()
			reg.closeClaims()
			err := <-errCh
			require.ErrorIs(t, err, errStartupWaitShutdown)
			assert.Equal(t, "terminal startup interrupted by shutdown", err.Error())
		})
	})

	t.Run("cancelForShutdown wakes blocked waiter via done with shutdown error", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			h := beginForTest(t, &reg.startupCore, "t-cancel-shutdown")
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(context.Background(), "t-cancel-shutdown") }()
			synctest.Wait()

			ids := reg.cancelForShutdown()
			assert.Equal(t, []string{"t-cancel-shutdown"}, ids)
			assert.True(t, reg.shutdownStopped(h))
			err := <-errCh
			require.ErrorIs(t, err, errStartupWaitShutdown)
			assert.Equal(t, "terminal startup interrupted by shutdown", err.Error())
		})
	})

	t.Run("WaitForStartup after closeClaims with active non-failed entry returns shutdown error", func(t *testing.T) {
		t.Parallel()
		reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
		beginForTest(t, &reg.startupCore, "t-closed-claims-active")
		reg.closeClaims()

		err := reg.WaitForStartup(t.Context(), "t-closed-claims-active")
		require.ErrorIs(t, err, errStartupWaitShutdown)
		assert.Equal(t, "terminal startup interrupted by shutdown", err.Error())
	})

	t.Run("agent registry uses agent wording in wait errors", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			clock := testutil.NewQuartzMock(t)
			reg := newAgentStartupRegistry(clock)

			hFail := beginForTest(t, &reg.startupCore, "a-fail")
			reg.fail(hFail, "missing binary")
			err := reg.WaitForStartup(context.Background(), "a-fail")
			require.ErrorIs(t, err, errStartupFailed)
			assert.Equal(t, "agent startup failed: missing binary", err.Error())

			beginForTest(t, &reg.startupCore, "a-close")
			closeCh := make(chan error, 1)
			go func() { closeCh <- reg.WaitForStartup(context.Background(), "a-close") }()
			synctest.Wait()
			reg.cancelAndClear("a-close", keepWorktreeOnClose)
			err = <-closeCh
			require.ErrorIs(t, err, errStartupWaitClosed)
			assert.Equal(t, "agent closed during startup", err.Error())

			beginForTest(t, &reg.startupCore, "a-archive")
			archiveCh := make(chan error, 1)
			go func() { archiveCh <- reg.WaitForStartup(context.Background(), "a-archive") }()
			synctest.Wait()
			reg.cancelForArchive("a-archive")
			err = <-archiveCh
			require.ErrorIs(t, err, errStartupWaitArchived)
			assert.Equal(t, "agent archived during startup", err.Error())

			beginForTest(t, &reg.startupCore, "a-shutdown")
			shutdownCh := make(chan error, 1)
			go func() { shutdownCh <- reg.WaitForStartup(context.Background(), "a-shutdown") }()
			synctest.Wait()
			reg.closeClaims()
			err = <-shutdownCh
			require.ErrorIs(t, err, errStartupWaitShutdown)
			assert.Equal(t, "agent startup interrupted by shutdown", err.Error())
		})
	})

	t.Run("ctx cancellation unblocks waiter", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
			beginForTest(t, &reg.startupCore, "t-cancel")
			waitCtx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() { errCh <- reg.WaitForStartup(waitCtx, "t-cancel") }()
			synctest.Wait()
			cancel()
			require.ErrorIs(t, <-errCh, context.Canceled)
		})
	})
}
