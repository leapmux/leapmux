package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/terminput"
)

func newTerminalAttachLeaf() *cobra.Command {
	cmd := cli.NewLeaf(cli.Leaf{
		Use:              "attach [terminal]",
		Short:            "Attach interactively to a remote terminal over E2EE (detach with Ctrl-])",
		QuietUnsupported: true,
		Long: `Attach this terminal interactively to a LeapMux terminal over the encrypted
Worker channel. Keystrokes travel straight to the remote PTY, output streams
live to stdout, and local window resizes forward automatically.

Press Ctrl-] (ASCII 0x1d, or the key set by --detach) to detach cleanly
without closing the remote shell.`,
		Examples: []cli.Example{
			{Command: "leapmux terminal attach 4cbb3f34"},
			{Comment: "Attach with Ctrl-b as the detach key.", Command: "leapmux terminal attach 4cbb3f34 --detach ctrl-b"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("detach", "ctrl-]", "key sequence that detaches without closing the remote shell (e.g. ctrl-], ctrl-b, ctrl-a)")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runTerminalAttach,
	})
	cli.DisallowInheritedFlags(cmd, "json", "format", "fields")
	return cmd
}

// parseDetachByte parses a single-byte control-key name like "ctrl-]" or
// "ctrl-b" using terminput and returns its raw byte.
func parseDetachByte(raw string) (byte, error) {
	trimmed := strings.TrimSpace(raw)
	key, err := terminput.ParseKey(trimmed)
	if err != nil {
		return 0, cli.Usagef("--detach must be a single-byte ctrl-<key> sequence such as ctrl-], ctrl-a, or ctrl-b; got %q", raw)
	}
	encoded := terminput.Input{Keys: []terminput.Key{key}}.Encode(terminput.Modes{})
	if !strings.HasPrefix(key.String(), "ctrl-") || len(encoded) != 1 {
		return 0, cli.Usagef("--detach must be a single-byte ctrl-<key> sequence such as ctrl-], ctrl-a, or ctrl-b; got %q", raw)
	}
	return encoded[0], nil
}

// terminalSizeFD selects the file descriptor to query for terminal dimensions.
// Checking stdout first ensures Windows GetConsoleScreenBufferInfo receives a
// console output handle rather than a stdin input handle.
func terminalSizeFD(out io.Writer, fallbackFD int) int {
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return int(f.Fd())
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return int(os.Stdout.Fd())
	}
	return fallbackFD
}

type attachRPCCall func(ctx context.Context, method string, req, resp proto.Message) error

func syncAttachSize(ctx context.Context, call attachRPCCall, terminalID string, getSize func() (cols, rows int, ok bool)) {
	cols, rows, ok := getSize()
	if !ok || cols <= 0 || rows <= 0 {
		return
	}
	rCtx, rCancel := rpcDeadline(ctx)
	_ = call(rCtx, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, &leapmuxv1.ResizeTerminalRequest{
		TerminalId: terminalID,
		Cols:       uint32(cols),
		Rows:       uint32(rows),
	}, nil)
	rCancel()
}

func sendAttachInput(ctx context.Context, call attachRPCCall, terminalID string, data []byte) error {
	sendCtx, sendCancel := rpcDeadline(ctx)
	defer sendCancel()
	return call(sendCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       data,
	}, nil)
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

type attachSessionDeps struct {
	openTransport func(ctx context.Context) (*workerWatchTransport, error)
	fallbackCall  attachRPCCall
	clock         quartz.Clock
	// prepareLocalTerminal optionally puts a local TTY into raw mode once the
	// remote terminal is confirmed live, and returns cleanup, a size reader,
	// and a resize notification channel.
	prepareLocalTerminal func() (cleanup func(), getSize func() (cols, rows int, ok bool), winch <-chan os.Signal, err error)
}

func runTerminalAttach(inv *cli.Invocation) (any, error) {
	detachByte, err := parseDetachByte(stringFlag(inv, "detach"))
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	callSession := s.timed()
	got, err := callSession.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err == nil {
		err = maybePreflightWorker(callSession.ctx, s.c, got.WorkerID)
	}
	callSession.Close()
	if err != nil {
		return nil, err
	}

	out := inv.Command.OutOrStdout()
	if inv.Printer != nil && inv.Printer.Out != nil {
		out = inv.Printer.Out
	}
	in := inv.Stdin
	if in == nil {
		in = os.Stdin
	}

	deps := attachSessionDeps{
		openTransport: func(ctx context.Context) (*workerWatchTransport, error) {
			return newWorkerWatchTransport(ctx, s.c, got.WorkerID)
		},
		fallbackCall: func(ctx context.Context, method string, req, resp proto.Message) error {
			return callInnerRPC(ctx, s.c, got.WorkerID, method, req, resp)
		},
		clock: quartz.NewReal(),
		prepareLocalTerminal: func() (func(), func() (int, int, bool), <-chan os.Signal, error) {
			f, ok := in.(*os.File)
			if !ok || !term.IsTerminal(int(f.Fd())) {
				return func() {}, nil, nil, nil
			}
			ttyFD := int(f.Fd())
			oldState, rawErr := term.MakeRaw(ttyFD)
			if rawErr != nil {
				return nil, nil, nil, cli.Wrap(cli.CodeInternal, rawErr, "put the local terminal into raw mode")
			}
			sizeFD := terminalSizeFD(out, ttyFD)
			winch, stopWinch := notifyResizeSignals()
			cleanup := func() {
				stopWinch()
				_ = term.Restore(ttyFD, oldState)
			}
			getSize := func() (int, int, bool) {
				cols, rows, err := term.GetSize(sizeFD)
				return cols, rows, err == nil && cols > 0 && rows > 0
			}
			return cleanup, getSize, winch, nil
		},
	}
	return nil, runTerminalAttachSession(s.ctx, got.ID, detachByte, in, out, deps)
}

// runTerminalAttachSession runs an interactive attach session over ONE shared
// workerWatchTransport tracked in activeTransport so ListTerminals,
// WatchEvents, ResizeTerminal, and SendInput share a single E2EE channel and
// survive watch reconnects.
func runTerminalAttachSession(
	parentCtx context.Context,
	terminalID string,
	detachByte byte,
	in io.Reader,
	out io.Writer,
	deps attachSessionDeps,
) error {
	clock := deps.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	initialWT, err := deps.openTransport(parentCtx)
	if err != nil {
		return err
	}
	var activeTransport atomic.Pointer[workerWatchTransport]
	activeTransport.Store(initialWT)

	// firstOpen tracks ownership of initialWT. Once the feed claims it on its
	// first openTransport call, the feed's release callback closes initialWT.
	// If this function returns before the feed claims it, the defer below
	// closes initialWT.
	var firstOpen atomic.Bool
	firstOpen.Store(true)
	defer func() {
		if firstOpen.Swap(false) {
			activeTransport.CompareAndSwap(initialWT, nil)
			initialWT.close()
		}
	}()

	infoCtx, infoCancel := rpcDeadline(parentCtx)
	info, err := readTerminalInfo(infoCtx, initialWT.Call, terminalID)
	infoCancel()
	if err != nil {
		return err
	}

	// Inspect status before entering raw mode or subscribing so an exited
	// or failed shell never puts the local terminal in raw mode or replays
	// its screen twice.
	switch info.GetStatus() {
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
		return terminalStartupFailedError(terminalID, info.GetStartupError())
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
		if len(info.GetScreen()) > 0 {
			if _, err := out.Write(info.GetScreen()); err != nil {
				return err
			}
		}
		return nil
	default:
	}

	var (
		getSize func() (cols, rows int, ok bool)
		winch   <-chan os.Signal
	)
	if deps.prepareLocalTerminal != nil {
		var cleanup func()
		cleanup, getSize, winch, err = deps.prepareLocalTerminal()
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
	}

	callRPC := func(callCtx context.Context, method string, req, resp proto.Message) error {
		if wt := activeTransport.Load(); wt != nil {
			return wt.Call(callCtx, method, req, resp)
		}
		if deps.fallbackCall != nil {
			return deps.fallbackCall(callCtx, method, req, resp)
		}
		return errWatchEnded
	}

	ctx, cancel := context.WithCancel(parentCtx)
	var auxWG sync.WaitGroup
	if getSize != nil {
		syncAttachSize(ctx, callRPC, terminalID, getSize)
		if winch != nil {
			auxWG.Go(func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-winch:
						syncAttachSize(ctx, callRPC, terminalID, getSize)
					}
				}
			})
		}
	}

	feedSource := screenFeedSource{
		openTransport: func(openCtx context.Context) (streamevents.Transport, func(), error) {
			var wt *workerWatchTransport
			if firstOpen.Swap(false) {
				wt = initialWT
			} else {
				var openErr error
				wt, openErr = deps.openTransport(openCtx)
				if openErr != nil {
					return nil, nil, openErr
				}
				activeTransport.Store(wt)
			}
			release := func() {
				activeTransport.CompareAndSwap(wt, nil)
				wt.close()
			}
			return wt.transport, release, nil
		},
		clock: clock,
	}

	var feedWG sync.WaitGroup
	items := make(chan screenFeedItem, 32)
	startTerminalOutputFeed(ctx, &feedWG, feedSource, terminalID, info.GetScreenEndOffset(), items)

	defer func() {
		cancel()
		feedWG.Wait()
		auxWG.Wait()
	}()

	sendFn := func(sendCtx context.Context, data []byte) error {
		return sendAttachInput(sendCtx, callRPC, terminalID, data)
	}
	return runAttachLoop(ctx, terminalID, info, detachByte, in, out, items, sendFn)
}

// clearScreenAndHome clears the local viewport and resets the cursor before
// writing a reconnect snapshot so old bytes do not bleed through.
const clearScreenAndHome = "\x1b[2J\x1b[H"

// runAttachLoop streams PTY output from items to out while forwarding stdin
// keystrokes asynchronously via sendInput in strict FIFO order until detach,
// shell exit, EOF, or context cancellation.
func runAttachLoop(
	parentCtx context.Context,
	terminalID string,
	info *leapmuxv1.TerminalInfo,
	detachByte byte,
	in io.Reader,
	out io.Writer,
	items <-chan screenFeedItem,
	sendInput func(context.Context, []byte) error,
) error {
	sawInitialScreen := len(info.GetScreen()) > 0
	initialEndOffset := info.GetScreenEndOffset()
	if sawInitialScreen {
		if _, err := out.Write(info.GetScreen()); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(parentCtx)

	// Send keystrokes on an ordered background goroutine so SendInput round-trip
	// latency never stalls incoming PTY output rendering.
	inputQueue := make(chan []byte, 64)
	inputErrCh := make(chan error, 1)
	var senderWG sync.WaitGroup
	senderWG.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-inputQueue:
				if !ok {
					return
				}
				if err := sendInput(ctx, data); err != nil {
					select {
					case inputErrCh <- err:
					default:
					}
					return
				}
			}
		}
	})

	// Decouple blocking stdin reads from the select loop. Interrupting a real
	// TTY os.Stdin read is not portable because SetReadDeadline returns
	// os.ErrNoDeadline on TTY devices, so the stdin goroutine is not joined on
	// teardown; pollable pipes, sockets, and test readers unblock promptly when
	// SetReadDeadline(time.Now()) fires below.
	type stdinChunk struct {
		data []byte
		err  error
	}
	stdinCh := make(chan stdinChunk, 16)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				select {
				case stdinCh <- stdinChunk{data: bytes.Clone(buf[:n])}:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				if ctx.Err() != nil && errors.Is(rerr, os.ErrDeadlineExceeded) {
					return
				}
				select {
				case stdinCh <- stdinChunk{err: rerr}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	defer func() {
		cancel()
		if dl, ok := in.(readDeadliner); ok {
			_ = dl.SetReadDeadline(time.Now())
		}
		senderWG.Wait()
	}()

	initialSnapshotPending := sawInitialScreen
	receivedOutput := sawInitialScreen
	for {
		select {
		case <-ctx.Done():
			return nil
		case sendErr := <-inputErrCh:
			return sendErr
		case sc := <-stdinCh:
			if len(sc.data) > 0 {
				chunk := sc.data
				if idx := bytes.IndexByte(chunk, detachByte); idx >= 0 {
					if idx > 0 {
						select {
						case inputQueue <- chunk[:idx]:
						case sendErr := <-inputErrCh:
							return sendErr
						case <-ctx.Done():
							return nil
						}
					}
					// Drain queued keystrokes in order before canceling ctx on detach.
					close(inputQueue)
					senderWG.Wait()
					select {
					case sendErr := <-inputErrCh:
						return sendErr
					default:
						return nil
					}
				}
				select {
				case inputQueue <- chunk:
				case sendErr := <-inputErrCh:
					return sendErr
				case <-ctx.Done():
					return nil
				}
			}
			if sc.err != nil {
				if errors.Is(sc.err, io.EOF) {
					close(inputQueue)
					senderWG.Wait()
					select {
					case sendErr := <-inputErrCh:
						return sendErr
					default:
						return nil
					}
				}
				return sc.err
			}
		case item := <-items:
			if done, feedErr := terminalFeedItemStatus(terminalID, item); done {
				return feedErr
			}
			if data := item.event.GetData(); data != nil {
				chunk := data.GetData()
				if data.GetIsSnapshot() {
					skipDuplicate := initialSnapshotPending && data.GetEndOffset() == initialEndOffset
					initialSnapshotPending = false
					if !skipDuplicate {
						if receivedOutput {
							if _, err := io.WriteString(out, clearScreenAndHome); err != nil {
								return err
							}
						}
						if len(chunk) > 0 {
							if _, err := out.Write(chunk); err != nil {
								return err
							}
						}
						receivedOutput = true
					}
				} else {
					if data.GetReplayComplete() || len(chunk) > 0 {
						initialSnapshotPending = false
					}
					if len(chunk) > 0 {
						receivedOutput = true
						if _, err := out.Write(chunk); err != nil {
							return err
						}
					}
				}
			}
			if sc := item.event.GetStatusChange(); sc != nil {
				switch sc.GetStatus() {
				case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
					return nil
				case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
					return terminalStartupFailedError(terminalID, sc.GetStartupError())
				default:
				}
			}
			if item.event.GetClosed() != nil {
				return nil
			}
		}
	}
}
