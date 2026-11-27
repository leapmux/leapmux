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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
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
	screen, _, _ := svc.Terminals.ScreenSnapshotSince(terminalID, 0)
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

func dispatchWithContext(t *testing.T, d testRPCDispatcher, ctx context.Context, method string, req proto.Message, w *testResponseWriter) {
	t.Helper()
	payload, err := proto.Marshal(req)
	require.NoError(t, err)
	dispatchTestRequest(d, ctx, channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method:  method,
		Payload: payload,
	}, w)
}

// doneObservingContext signals on observed the first time a caller reads
// Done(), which lets a test synchronize on WaitForStartup entering its select
// without sleeping.
type doneObservingContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func newDoneObservingContext(parent context.Context) (*doneObservingContext, <-chan struct{}) {
	ch := make(chan struct{})
	return &doneObservingContext{Context: parent, observed: ch}, ch
}

func (c *doneObservingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func openGatedTerminal(t *testing.T, svc *Service, d testRPCDispatcher, startFn func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error) (terminalID string, entered <-chan struct{}, proceed chan struct{}) {
	t.Helper()
	enteredCh := make(chan struct{})
	proceedCh := make(chan struct{})
	var once sync.Once
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		once.Do(func() { close(enteredCh) })
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
	return id, enteredCh, proceedCh
}

// SendInput blocks while PTY startup is in flight and delivers input once
// startup transitions to READY.
func TestSendInput_WaitsForInFlightStartupAndDeliversInput(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	terminalID, _, proceed := openGatedTerminal(t, svc, d, svc.Terminals.StartTerminal)
	t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })
	require.False(t, svc.Terminals.HasTerminal(terminalID), "precondition: PTY is not registered yet")

	line, err := testutil.TestShellOutputCommand("delivered_after_startup")
	require.NoError(t, err)

	waitCtx, enteredWait := newDoneObservingContext(ctx)
	wSend := newTestWriter()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		dispatchWithContext(t, d, waitCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
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

	close(proceed)

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

	terminalID, _, proceed := openGatedTerminal(t, svc, d, func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("pty spawn failed")
	})

	waitCtx, enteredWait := newDoneObservingContext(ctx)
	wWait := newTestWriter()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		dispatchWithContext(t, d, waitCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
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
	close(proceed)

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
	dispatchWithContext(t, d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte("echo still-failed"),
		Keys:       []string{"Enter"},
	}, wAfter)
	require.Len(t, wAfter.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wAfter.errors[0].code)
	assert.Contains(t, wAfter.errors[0].message, "pty spawn failed")
	assert.Empty(t, wAfter.responses)
}

// SendInput honors ctx cancellation during startup wait and leaves no in-flight
// sendInputReceipts entry behind, so a retry with the same request_id succeeds
// once startup finishes.
func TestSendInput_ContextCanceledDuringStartupWaitAllowsRetryWithSameRequestID(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	terminalID, _, proceed := openGatedTerminal(t, svc, d, svc.Terminals.StartTerminal)
	t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })

	line, err := testutil.TestShellOutputCommand("retried_after_cancel")
	require.NoError(t, err)
	const reqID = "req-cancel-then-retry"
	req := &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte(line),
		Paste:      true,
		Keys:       []string{"Enter"},
		RequestId:  reqID,
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	waitCtx, enteredWait := newDoneObservingContext(cancelCtx)
	wCanceled := newTestWriter()
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		dispatchWithContext(t, d, waitCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wCanceled)
	}()
	select {
	case <-enteredWait:
	case <-ctx.Done():
		t.Fatal("SendInput did not enter WaitForStartup")
	}
	cancel()
	select {
	case <-cancelDone:
	case <-ctx.Done():
		t.Fatal("SendInput did not exit after ctx cancellation")
	}
	require.Len(t, wCanceled.errors, 1)
	assert.Equal(t, int32(codes.Canceled), wCanceled.errors[0].code)
	assert.Empty(t, wCanceled.responses)

	// Confirm no receipt entry was reserved for the canceled attempt.
	svc.sendInputReceipts.mu.Lock()
	_, reserved := svc.sendInputReceipts.entries[terminalID+"\x00"+reqID]
	svc.sendInputReceipts.mu.Unlock()
	assert.False(t, reserved, "canceled startup wait must not reserve a sendInputReceipts entry")

	// Unblock startup and retry with the exact same request_id.
	close(proceed)
	wRetry := newTestWriter()
	dispatchWithContext(t, d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wRetry)
	require.Empty(t, wRetry.errors)
	require.Len(t, wRetry.responses, 1)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, terminalID, "retried_after_cancel")
	}, "retry with same request_id delivered input once startup reached READY")
}

// manualDeadlineContext triggers context.DeadlineExceeded deterministically
// when expire is called, avoiding wall-clock sleeps or short timeouts.
type manualDeadlineContext struct {
	context.Context
	done  chan struct{}
	once  sync.Once
	fired atomic.Bool
}

func newManualDeadlineContext(parent context.Context) (*manualDeadlineContext, func()) {
	c := &manualDeadlineContext{
		Context: parent,
		done:    make(chan struct{}),
	}
	expire := func() {
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

// SendInput returns codes.DeadlineExceeded when ctx expires during startup wait
// without reserving a sendInputReceipts entry, so a retry with the same
// request_id succeeds once startup finishes.
func TestSendInput_DeadlineExceededDuringStartupWaitAllowsRetryWithSameRequestID(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	terminalID, _, proceed := openGatedTerminal(t, svc, d, svc.Terminals.StartTerminal)
	t.Cleanup(func() { svc.Terminals.RemoveTerminal(terminalID) })

	line, err := testutil.TestShellOutputCommand("retried_after_deadline")
	require.NoError(t, err)
	const reqID = "req-deadline-then-retry"
	req := &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte(line),
		Paste:      true,
		Keys:       []string{"Enter"},
		RequestId:  reqID,
	}

	deadlineCtx, expire := newManualDeadlineContext(ctx)
	t.Cleanup(expire)
	waitCtx, enteredWait := newDoneObservingContext(deadlineCtx)
	wTimedOut := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchWithContext(t, d, waitCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wTimedOut)
	}()
	select {
	case <-enteredWait:
	case <-ctx.Done():
		t.Fatal("SendInput did not enter WaitForStartup")
	}
	expire()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("SendInput did not exit after deadline expired")
	}
	require.Len(t, wTimedOut.errors, 1)
	assert.Equal(t, int32(codes.DeadlineExceeded), wTimedOut.errors[0].code)
	assert.Empty(t, wTimedOut.responses)

	// Confirm no receipt entry was reserved for the timed-out attempt.
	svc.sendInputReceipts.mu.Lock()
	_, reserved := svc.sendInputReceipts.entries[terminalID+"\x00"+reqID]
	svc.sendInputReceipts.mu.Unlock()
	assert.False(t, reserved, "timed-out startup wait must not reserve a sendInputReceipts entry")

	// Unblock startup and retry with the exact same request_id.
	close(proceed)
	wRetry := newTestWriter()
	dispatchWithContext(t, d, ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, wRetry)
	require.Empty(t, wRetry.errors)
	require.Len(t, wRetry.responses, 1)
	testutil.AssertEventually(t, func() bool {
		return screenContains(t, svc, terminalID, "retried_after_deadline")
	}, "retry with same request_id delivered input once startup reached READY")
}

func TestTerminalStartup_WaitForStartupOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("no active entry returns nil immediately", func(t *testing.T) {
		t.Parallel()
		reg := newTerminalStartupRegistry(testutil.NewQuartzMock(t))
		require.NoError(t, reg.WaitForStartup(t.Context(), "t-none"))
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
			assert.Contains(t, err.Error(), "shell missing")
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
			require.ErrorIs(t, <-errCh, errStartupWaitClosed)
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
			require.ErrorIs(t, <-errCh, errStartupWaitArchived)
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
			require.ErrorIs(t, <-errCh, errStartupWaitShutdown)
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
