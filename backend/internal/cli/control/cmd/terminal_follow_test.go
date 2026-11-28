package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestTerminalChunkSanitizer_StreamingCases(t *testing.T) {
	for name, tc := range map[string]struct {
		chunks []string
		want   string
	}{
		"trailing ESC inside OSC stays in OSC when followed by non-backslash": {
			chunks: []string{"a\x1b]0;title\x1b", "xmore\x1b\\b"},
			want:   "ab",
		},
		"trailing ESC inside OSC finishes when followed by backslash": {
			chunks: []string{"a\x1b]0;title\x1b", "\\b"},
			want:   "ab",
		},
		"inner complete CSI inside OSC does not end OSC": {
			chunks: []string{"a\x1b]0;\x1b[31mred\x1b[0m", "still-title\x07b"},
			want:   "ab",
		},
		"malformed CSI with newline aborts immediately": {
			chunks: []string{"a\x1b[12\nb"},
			want:   "a\nb",
		},
		"ESC # 8 and ESC % G across chunks": {
			chunks: []string{"a\x1b#", "8b\x1b%", "Gc"},
			want:   "abc",
		},
		"8-bit C1 CSI and ST split across 0xC2 boundary": {
			chunks: []string{"hi\xc2", "\x9b31m!\x1bPdata\xc2", "\x9cdone"},
			want:   "hi!done",
		},
		"2-byte UTF-8 starting with 0xC2 split across chunks": {
			chunks: []string{"copy \xc2", "\xa9 2026"},
			want:   "copy © 2026",
		},
		"3-byte and 4-byte UTF-8 split byte by byte": {
			chunks: []string{"\xe6", "\x97", "\xa5", "\xf0", "\x9f", "\x98", "\x80"},
			want:   "日😀",
		},
		"split BiDi override stripped cleanly": {
			chunks: []string{"safe\xe2\x80", "\xaetext"},
			want:   "safetext",
		},
		"incomplete UTF-8 flushed as replacement rune at EOF": {
			chunks: []string{"hi\xe2\x80"},
			want:   "hi\ufffd\ufffd",
		},
		"trailing lone 0xC2 flushed as replacement rune at EOF": {
			chunks: []string{"hi\xc2"},
			want:   "hi\ufffd",
		},
		"incomplete escape at EOF produces no leak": {
			chunks: []string{"ok\x1b[38;5;208"},
			want:   "ok",
		},
		"strips CR and preserves LF and tab": {
			chunks: []string{"a\r\nb\tc\r"},
			want:   "a\nb\tc",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := cli.NewTerminalSanitizer()
			var got strings.Builder
			for _, c := range tc.chunks {
				got.WriteString(s.Write([]byte(c)))
			}
			got.WriteString(s.Flush())
			assert.Equal(t, tc.want, got.String())
		})
	}

	t.Run("64 KiB OSC 52 payload split across chunks never leaks", func(t *testing.T) {
		s := cli.NewTerminalSanitizer()
		var got strings.Builder
		got.WriteString(s.Write([]byte("before\x1b]52;c;")))
		block := bytes.Repeat([]byte("A"), 1024)
		for range 64 {
			assert.Empty(t, s.Write(block))
		}
		got.WriteString(s.Write([]byte("\x07after")))
		got.WriteString(s.Flush())
		assert.Equal(t, "beforeafter", got.String())
	})

	t.Run("reset clears partial UTF-8 and partial OSC state cleanly", func(t *testing.T) {
		s := cli.NewTerminalSanitizer()
		assert.Equal(t, "pre", s.Write([]byte("pre\xe2\x80")))
		s.Reset()
		assert.Equal(t, "ok", s.Write([]byte("ok"))+s.Flush())

		assert.Equal(t, "", s.Write([]byte("\x1b]0;unterminated")))
		s.Reset()
		assert.Equal(t, "fresh", s.Write([]byte("fresh"))+s.Flush())
	})
}

func TestParseDetachByte_DelegatesToTerminput(t *testing.T) {
	for input, want := range map[string]byte{
		"ctrl-a":     0x01,
		"CTRL-B":     0x02,
		"ctrl-z":     0x1a,
		"ctrl-[":     0x1b,
		"ctrl-\\":    0x1c,
		"ctrl-]":     0x1d,
		"ctrl-^":     0x1e,
		"ctrl-_":     0x1f,
		"ctrl-@":     0x00,
		"ctrl-space": 0x00,
		"  ctrl-] ":  0x1d,
	} {
		got, err := parseDetachByte(input)
		require.NoErrorf(t, err, "input %q", input)
		assert.Equalf(t, want, got, "input %q", input)
	}

	for _, bad := range []string{"", "a", "enter", "alt-a", "ctrl-alt-a", "ctrl-left", "ctrl-", "bad"} {
		_, err := parseDetachByte(bad)
		require.Errorf(t, err, "input %q must fail", bad)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	}
}

func testFollowInvocation(format cli.Format, out io.Writer) *cli.Invocation {
	cmd := &cobra.Command{Use: "read"}
	cmd.SetOut(io.Discard)
	return &cli.Invocation{
		Command: cmd,
		Printer: &cli.Printer{Out: out, Err: io.Discard, Format: format},
	}
}

func TestFollowTerminalFromSource_DeferOrderStatusSnapshotAndFormats(t *testing.T) {
	t.Run("exits cleanly on Closed without LIFO defer deadlock and writes to Printer.Out", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		source := screenFeedSource{
			openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
			clock:         testutil.NewQuartzMock(t),
		}
		var out bytes.Buffer
		inv := testFollowInvocation(cli.FormatText, &out)
		info := screenOf(40, 5, "initial\r\n")
		info.TerminalId = "t-1"
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY

		done := make(chan error, 1)
		go func() {
			done <- followTerminalFromSource(inv, ctx, source, "t-1", info, screenReadOptions{})
		}()

		receive(t, ctx, tr.opened)
		requireOutputTerminalEntry(t, tr.lastRequest(), info.GetScreenEndOffset())
		// Duplicate initial snapshot with same end offset must not duplicate output.
		tr.push(terminalFrame("t-1", outputReplayDataEvent("initial\r\n", info.GetScreenEndOffset(), true)))
		// Live chunk + Closed must return without hanging on wg.Wait().
		tr.push(terminalFrame("t-1", dataEvent("\x1b[32mlive\x1b[0m\n", info.GetScreenEndOffset()+14, false)))
		tr.push(terminalFrame("t-1", &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
		}))

		require.NoError(t, <-done)
		assert.Equal(t, "initial\nlive\n", out.String())
	})

	t.Run("handles StatusChange EXITED and STARTUP_FAILED", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			status  leapmuxv1.TerminalStatus
			errMsg  string
			wantErr string
		}{
			{name: "exited", status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
			{name: "startup_failed", status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, errMsg: "exec failed", wantErr: "exec failed"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx := testutil.DeadlineContext(t)
				tr := newFakeWatchTransport()
				source := screenFeedSource{
					openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
					clock:         testutil.NewQuartzMock(t),
				}
				var out bytes.Buffer
				inv := testFollowInvocation(cli.FormatText, &out)
				info := screenOf(40, 5, "")
				info.TerminalId = "t-1"
				info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY

				done := make(chan error, 1)
				go func() {
					done <- followTerminalFromSource(inv, ctx, source, "t-1", info, screenReadOptions{})
				}()
				receive(t, ctx, tr.opened)
				tr.push(terminalFrame("t-1", outputReplayDataEvent("", 0, true)))
				tr.push(terminalFrame("t-1", &leapmuxv1.TerminalEvent{
					TerminalId: "t-1",
					Event: &leapmuxv1.TerminalEvent_StatusChange{
						StatusChange: &leapmuxv1.TerminalStatusChange{
							Status:       tc.status,
							StartupError: tc.errMsg,
						},
					},
				}))
				err := <-done
				if tc.wantErr == "" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tc.wantErr)
				}
			})
		}
	})

	t.Run("reconnect snapshot resets sanitizer so stale OSC does not eat snapshot", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		source := screenFeedSource{
			openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
			clock:         testutil.NewQuartzMock(t),
		}
		var out bytes.Buffer
		inv := testFollowInvocation(cli.FormatText, &out)
		info := screenOf(40, 5, "")
		info.TerminalId = "t-1"
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY

		done := make(chan error, 1)
		go func() {
			done <- followTerminalFromSource(inv, ctx, source, "t-1", info, screenReadOptions{})
		}()
		receive(t, ctx, tr.opened)
		// Replay leaves sanitizer inside an unterminated OSC.
		tr.push(terminalFrame("t-1", outputReplayDataEvent("a\x1b]0;cut", 8, false)))
		// Reconnect snapshot must reset sanitizer and emit cleanly.
		tr.push(terminalFrame("t-1", outputReplayDataEvent("recovered", 20, true)))
		tr.push(terminalFrame("t-1", &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
		}))
		require.NoError(t, <-done)
		assert.Equal(t, "arecovered", out.String())
	})

	t.Run("routes TSV format through Printer.Event and encodes raw invalid UTF-8 as base64 in JSON", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		source := screenFeedSource{
			openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
			clock:         testutil.NewQuartzMock(t),
		}
		var tsvOut bytes.Buffer
		invTSV := testFollowInvocation(cli.FormatTSV, &tsvOut)
		info := screenOf(40, 5, "hello\r\n")
		info.TerminalId = "t-1"
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED
		require.NoError(t, followTerminalFromSource(invTSV, ctx, source, "t-1", info, screenReadOptions{}))
		assert.Contains(t, tsvOut.String(), "t-1\t")

		// Raw mode with invalid UTF-8 bytes in JSON carries lossless data_base64 and omits corrupted text.
		rawBytes := []byte{0xff, 0xfe, 0x1b, '[', '3', '1', 'm'}
		var jsonOut bytes.Buffer
		invJSON := testFollowInvocation(cli.FormatJSON, &jsonOut)
		rawInfo := &leapmuxv1.TerminalInfo{
			TerminalId: "t-1",
			Cols:       40,
			Rows:       5,
			Screen:     rawBytes,
			Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
		}
		require.NoError(t, followTerminalFromSource(invJSON, ctx, source, "t-1", rawInfo, screenReadOptions{raw: true}))
		var env struct {
			Data terminalFollowChunk `json:"data"`
		}
		require.NoError(t, json.Unmarshal(bytes.TrimSpace(jsonOut.Bytes()), &env))
		assert.True(t, env.Data.Raw)
		assert.Empty(t, env.Data.Text, "invalid UTF-8 raw bytes must not be mangled into text")
		decoded, err := base64.StdEncoding.DecodeString(env.Data.DataBase64)
		require.NoError(t, err)
		assert.Equal(t, rawBytes, decoded)
	})
}

func TestTerminalFeedItemStatus_RejectionsAndOverflow(t *testing.T) {
	done, err := terminalFeedItemStatus("t-1", screenFeedItem{
		rejected: &leapmuxv1.WatchRejection{
			EntityId: "t-1",
			Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		},
	})
	assert.True(t, done)
	assert.NoError(t, err, "NOT_FOUND during attach/follow exits cleanly")

	done, err = terminalFeedItemStatus("t-1", screenFeedItem{
		rejected: &leapmuxv1.WatchRejection{
			EntityId: "t-1",
			Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN,
		},
	})
	assert.True(t, done)
	require.Error(t, err)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	assert.Equal(t, cli.ExitAuth, cli.FromError(err).ExitCode())

	done, err = terminalFeedItemStatus("t-1", screenFeedItem{overflow: true})
	assert.True(t, done)
	require.Error(t, err)
	ce := cli.FromError(err)
	assert.Equal(t, cli.CodeInternal, ce.Code)
	assert.NotEqual(t, cli.ExitWaitEnded, ce.ExitCode(), "overflow must not report ExitWaitEnded")
}

func TestRunAttachLoop_DetachAsyncSendSnapshotsAndDeadlines(t *testing.T) {
	t.Run("flushes bytes before detach key in order and stops cleanly", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "prompt$ ")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 8)
		var out bytes.Buffer
		var (
			mu   sync.Mutex
			sent [][]byte
		)
		in := bytes.NewReader([]byte("ls\r\x1dignored"))
		err := runAttachLoop(ctx, "t-1", info, 0x1d, in, &out, items, func(_ context.Context, data []byte) error {
			mu.Lock()
			sent = append(sent, bytes.Clone(data))
			mu.Unlock()
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, "prompt$ ", out.String())
		mu.Lock()
		assert.Equal(t, [][]byte{[]byte("ls\r")}, sent)
		mu.Unlock()
	})

	t.Run("slow SendInput does not stall PTY output rendering and unblocks net.Pipe stdin on Closed", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "init")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 8)
		serverConn, clientConn := net.Pipe()
		defer func() { _ = serverConn.Close() }()
		defer func() { _ = clientConn.Close() }()

		var outMu sync.Mutex
		var outBuf bytes.Buffer
		outReceived := make(chan struct{}, 4)
		outWriter := writerFunc(func(p []byte) (int, error) {
			outMu.Lock()
			n, err := outBuf.Write(p)
			outMu.Unlock()
			select {
			case outReceived <- struct{}{}:
			default:
			}
			return n, err
		})

		sendStarted := make(chan struct{}, 1)
		releaseSend := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- runAttachLoop(ctx, "t-1", info, 0x1d, serverConn, outWriter, items, func(sendCtx context.Context, _ []byte) error {
				select {
				case sendStarted <- struct{}{}:
				default:
				}
				select {
				case <-releaseSend:
					return nil
				case <-sendCtx.Done():
					return sendCtx.Err()
				}
			})
		}()

		// Drain initial write notification.
		<-outReceived

		// Trigger a blocked SendInput.
		_, err := clientConn.Write([]byte("abc"))
		require.NoError(t, err)
		<-sendStarted

		// While SendInput is blocked, PTY output must still render immediately.
		// First send duplicate initial snapshot (must be skipped), then a live chunk, then a reconnect snapshot
		// with mode bytes already embedded in Data by ScreenSnapshotSince.
		items <- screenFeedItem{event: outputReplayDataEvent("init", info.GetScreenEndOffset(), true)}
		items <- screenFeedItem{event: dataEvent("-live", info.GetScreenEndOffset()+5, false)}
		<-outReceived

		snap := outputReplayDataEvent("\x1b[?1049hreconnected", 100, true)
		items <- screenFeedItem{event: snap}
		<-outReceived

		// Close remote shell; SetReadDeadline must unblock serverConn.Read without hanging.
		items <- screenFeedItem{event: &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
		}}
		close(releaseSend)
		require.NoError(t, <-done)

		outMu.Lock()
		got := outBuf.String()
		outMu.Unlock()
		assert.Equal(t, "init-live"+clearScreenAndHome+"\x1b[?1049hreconnected", got)
	})

	t.Run("propagates SendInput error and StatusChange STARTUP_FAILED", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 4)
		var out bytes.Buffer

		err := runAttachLoop(ctx, "t-1", info, 0x1d, bytes.NewReader([]byte("x")), &out, items, func(context.Context, []byte) error {
			return errors.New("send boom")
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "send boom")

		items2 := make(chan screenFeedItem, 4)
		items2 <- screenFeedItem{event: &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event: &leapmuxv1.TerminalEvent_StatusChange{
				StatusChange: &leapmuxv1.TerminalStatusChange{
					Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
					StartupError: "failed late",
				},
			},
		}}
		serverConn, clientConn := net.Pipe()
		defer func() { _ = serverConn.Close() }()
		defer func() { _ = clientConn.Close() }()
		err = runAttachLoop(ctx, "t-1", info, 0x1d, serverConn, &out, items2, func(context.Context, []byte) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed late")
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestTerminalAttachSession_SharesSingleChannelAndSurvivesReconnect(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reconnectTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer reconnectTimer.Close()

	var (
		mu          sync.Mutex
		methodsByCh = map[int][]string{}
		sendCh      = make(chan int, 4)
		resizeCh    = make(chan int, 4)
	)
	makeDispatcher := func(channelNum int) funcDispatcher {
		return funcDispatcher(func(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			mu.Lock()
			methodsByCh[channelNum] = append(methodsByCh[channelNum], req.GetMethod())
			mu.Unlock()
			switch req.GetMethod() {
			case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
				writeProtoResponse(rw, &leapmuxv1.ListTerminalsResponse{
					Terminals: []*leapmuxv1.TerminalInfo{{
						TerminalId:      "t-1",
						Cols:            80,
						Rows:            24,
						Screen:          []byte("$ "),
						ScreenEndOffset: 2,
						Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
					}},
				})
			case leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure:
				writeProtoResponse(rw, &leapmuxv1.ResizeTerminalResponse{})
				resizeCh <- channelNum
			case leapmuxv1connect.WorkerTerminalServiceSendInputProcedure:
				writeProtoResponse(rw, &leapmuxv1.SendInputResponse{})
				sendCh <- channelNum
			default:
				_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
			}
		})
	}

	// Wire two distinct workerWatchTransports (channel 1 initial, channel 2 after
	// reconnect), each backed by its own IPC peer so we can prove which transport
	// handled every ListTerminals, WatchEvents, ResizeTerminal, and SendInput.
	var (
		openCount atomic.Int32
		tr1       = newFakeWatchTransport()
		tr2       = newFakeWatchTransport()
		closed1   atomic.Bool
		closed2   atomic.Bool
	)
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, makeDispatcher(1))
	peer1, err := newClient(&cli.Invocation{Command: &cobra.Command{}})
	require.NoError(t, err)
	wt1, err := newWorkerWatchTransport(ctx, peer1, "worker-A")
	require.NoError(t, err)
	wt1.transport = tr1
	wt1.close = func() { closed1.Store(true) }

	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, makeDispatcher(2))
	peer2, err := newClient(&cli.Invocation{Command: &cobra.Command{}})
	require.NoError(t, err)
	wt2, err := newWorkerWatchTransport(ctx, peer2, "worker-A")
	require.NoError(t, err)
	wt2.transport = tr2
	wt2.close = func() { closed2.Store(true) }

	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()
	winch := make(chan os.Signal, 2)
	var outBuf bytes.Buffer
	var outMu sync.Mutex
	out := writerFunc(func(p []byte) (int, error) {
		outMu.Lock()
		defer outMu.Unlock()
		return outBuf.Write(p)
	})

	deps := attachSessionDeps{
		openTransport: func(context.Context) (*workerWatchTransport, error) {
			switch openCount.Add(1) {
			case 1:
				return wt1, nil
			case 2:
				return wt2, nil
			default:
				return nil, errors.New("unexpected third transport open")
			}
		},
		fallbackCall: func(context.Context, string, proto.Message, proto.Message) error {
			return errors.New("unexpected fallback RPC outside activeTransport")
		},
		clock: clock,
		prepareLocalTerminal: func() (func(), func() (int, int, bool), <-chan os.Signal, error) {
			return func() {}, func() (int, int, bool) { return 100, 30, true }, winch, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runTerminalAttachSession(ctx, "t-1", 0x1d, serverConn, out, deps)
	}()

	// Channel 1 handles initial ListTerminals, initial ResizeTerminal, WatchEvents
	// (with ScreenGeometry == false), and first SendInput — ONE channel total.
	assert.Equal(t, 1, receive(t, ctx, resizeCh))
	receive(t, ctx, tr1.opened)
	requireOutputTerminalEntry(t, tr1.lastRequest(), 2)
	tr1.push(terminalFrame("t-1", outputReplayDataEvent("", 2, false)))
	assert.Equal(t, int32(1), openCount.Load(), "attach must open exactly one transport before reconnect")

	_, err = clientConn.Write([]byte("a"))
	require.NoError(t, err)
	assert.Equal(t, 1, receive(t, ctx, sendCh))

	// Drop WatchEvents on channel 1 -> reconnect opens channel 2, closes channel 1,
	// and updates activeTransport so subsequent ResizeTerminal and SendInput use channel 2.
	tr1.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, reconnectTimer)).MustWait(ctx)
	receive(t, ctx, tr2.opened)
	requireOutputTerminalEntry(t, tr2.lastRequest(), 2)
	assert.True(t, closed1.Load(), "first transport must close on reconnect")
	tr2.push(terminalFrame("t-1", outputReplayDataEvent("hi", 4, false)))

	winch <- os.Interrupt
	assert.Equal(t, 2, receive(t, ctx, resizeCh), "SIGWINCH after reconnect must route through channel 2")

	_, err = clientConn.Write([]byte("b\x1d"))
	require.NoError(t, err)
	assert.Equal(t, 2, receive(t, ctx, sendCh), "SendInput after reconnect must route through channel 2")

	require.NoError(t, <-done)
	assert.True(t, closed2.Load(), "active transport must close on detach")
	assert.Equal(t, int32(2), openCount.Load())

	mu.Lock()
	ch1Methods := append([]string(nil), methodsByCh[1]...)
	ch2Methods := append([]string(nil), methodsByCh[2]...)
	mu.Unlock()
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure,
		leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure,
		leapmuxv1connect.WorkerTerminalServiceSendInputProcedure,
	}, ch1Methods)
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure,
		leapmuxv1connect.WorkerTerminalServiceSendInputProcedure,
	}, ch2Methods)
}

func TestFollowTerminalFromSource_ValidationStartupFailedAndRawUTF8(t *testing.T) {
	unopenedSource := func(t *testing.T) screenFeedSource {
		return screenFeedSource{
			openTransport: func(context.Context) (streamevents.Transport, func(), error) {
				t.Fatal("openTransport must not be called")
				return nil, nil, errors.New("unexpected open")
			},
			clock: testutil.NewQuartzMock(t),
		}
	}

	t.Run("--quiet with --follow fails with CodeUsage", func(t *testing.T) {
		var out bytes.Buffer
		inv := testFollowInvocation(cli.FormatQuiet, &out)
		info := screenOf(40, 5, "hi")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		err := followTerminalFromSource(inv, testutil.DeadlineContext(t), unopenedSource(t), "t-1", info, screenReadOptions{})
		require.Error(t, err)
		ce := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, ce.Code)
		assert.Contains(t, ce.Message, "--quiet cannot be used with --follow")
	})

	t.Run("initial STARTUP_FAILED returns terminalStartupFailedError without opening watch", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			detail  string
			wantMsg string
		}{
			{name: "with detail", detail: "bad shell", wantMsg: "terminal t-1 failed to start: bad shell"},
			{name: "without detail", detail: "", wantMsg: "terminal t-1 failed to start"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var out bytes.Buffer
				inv := testFollowInvocation(cli.FormatText, &out)
				info := &leapmuxv1.TerminalInfo{
					TerminalId:   "t-1",
					Cols:         40,
					Rows:         5,
					Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
					StartupError: tc.detail,
				}
				err := followTerminalFromSource(inv, testutil.DeadlineContext(t), unopenedSource(t), "t-1", info, screenReadOptions{})
				require.Error(t, err)
				ce := cli.FromError(err)
				assert.Equal(t, cli.CodeInternal, ce.Code)
				assert.Equal(t, tc.wantMsg, ce.Message)
			})
		}
	})

	t.Run("--raw with valid UTF-8 in JSON sets both data_base64 and text", func(t *testing.T) {
		rawBytes := []byte("\x1b[32mhello UTF-8 ✓\x1b[0m\n")
		var jsonOut bytes.Buffer
		inv := testFollowInvocation(cli.FormatJSON, &jsonOut)
		info := &leapmuxv1.TerminalInfo{
			TerminalId: "t-1",
			Cols:       40,
			Rows:       5,
			Screen:     rawBytes,
			Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
		}
		require.NoError(t, followTerminalFromSource(inv, testutil.DeadlineContext(t), unopenedSource(t), "t-1", info, screenReadOptions{raw: true}))
		var env struct {
			Data terminalFollowChunk `json:"data"`
		}
		require.NoError(t, json.Unmarshal(bytes.TrimSpace(jsonOut.Bytes()), &env))
		assert.True(t, env.Data.Raw)
		assert.Equal(t, string(rawBytes), env.Data.Text)
		decoded, err := base64.StdEncoding.DecodeString(env.Data.DataBase64)
		require.NoError(t, err)
		assert.Equal(t, rawBytes, decoded)
	})
}

type chunkedReader struct {
	chunks [][]byte
	err    error
	onEnd  func()
	index  int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.index < len(r.chunks) {
		n := copy(p, r.chunks[r.index])
		r.index++
		return n, nil
	}
	if r.onEnd != nil {
		fn := r.onEnd
		r.onEnd = nil
		fn()
	}
	return 0, r.err
}

func TestRunAttachLoop_EOFReadErrorAndInitialSnapshot(t *testing.T) {
	t.Run("stdin EOF drains queued keystrokes in order and returns nil", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 4)
		var out bytes.Buffer
		eofReached := make(chan struct{})
		in := &chunkedReader{
			chunks: [][]byte{[]byte("first"), []byte("second")},
			err:    io.EOF,
			onEnd:  func() { close(eofReached) },
		}
		var (
			mu   sync.Mutex
			sent [][]byte
		)
		err := runAttachLoop(ctx, "t-1", info, 0x1d, in, &out, items, func(_ context.Context, data []byte) error {
			<-eofReached
			mu.Lock()
			sent = append(sent, bytes.Clone(data))
			mu.Unlock()
			return nil
		})
		require.NoError(t, err)
		mu.Lock()
		assert.Equal(t, [][]byte{[]byte("first"), []byte("second")}, sent)
		mu.Unlock()
	})

	t.Run("stdin EOF returns pending inputErrCh error", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 4)
		var out bytes.Buffer
		eofReached := make(chan struct{})
		sendErr := errors.New("input failed during drain")
		in := &chunkedReader{
			chunks: [][]byte{[]byte("cmd\r")},
			err:    io.EOF,
			onEnd:  func() { close(eofReached) },
		}
		err := runAttachLoop(ctx, "t-1", info, 0x1d, in, &out, items, func(context.Context, []byte) error {
			<-eofReached
			return sendErr
		})
		require.ErrorIs(t, err, sendErr)
	})

	t.Run("non-EOF stdin read error returns directly", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 4)
		var out bytes.Buffer
		readErr := errors.New("stdin device closed")
		in := &chunkedReader{err: readErr}
		err := runAttachLoop(ctx, "t-1", info, 0x1d, in, &out, items, func(context.Context, []byte) error {
			return nil
		})
		require.ErrorIs(t, err, readErr)
	})

	t.Run("initial snapshot without prior output omits clearScreenAndHome", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		info := screenOf(80, 24, "")
		info.Status = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
		items := make(chan screenFeedItem, 4)
		items <- screenFeedItem{event: outputReplayDataEvent("first-snap", 10, true)}
		items <- screenFeedItem{event: outputReplayDataEvent("second-snap", 21, true)}
		items <- screenFeedItem{event: &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
		}}
		serverConn, clientConn := net.Pipe()
		defer func() { _ = serverConn.Close() }()
		defer func() { _ = clientConn.Close() }()
		var out bytes.Buffer
		err := runAttachLoop(ctx, "t-1", info, 0x1d, serverConn, &out, items, func(context.Context, []byte) error {
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, "first-snap"+clearScreenAndHome+"second-snap", out.String())
	})
}
