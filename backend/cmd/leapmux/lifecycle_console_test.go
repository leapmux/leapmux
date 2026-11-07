package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/procutil"
)

func testConsoleFrame(stream byte, payload []byte) []byte {
	frame := make([]byte, 5, 5+len(payload))
	frame[0] = stream
	binary.LittleEndian.PutUint32(frame[1:], uint32(len(payload)))
	return append(frame, payload...)
}

func TestConsoleRelayRoundTripsBothStreamsAndLargeWrites(t *testing.T) {
	connection, input := net.Pipe()
	ctx := testutil.DeadlineContext(t)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.NoError(t, connection.SetDeadline(deadline))
	require.NoError(t, input.SetDeadline(deadline))
	relay := &consoleRelay{connection: connection, context: context.Background(), clock: quartz.NewReal()}
	done := make(chan error, 1)
	joined := false
	t.Cleanup(func() {
		_ = connection.Close()
		_ = input.Close()
		if !joined {
			cleanupErr := <-done
			if !t.Failed() {
				require.NoError(t, cleanupErr)
			}
		}
	})
	large := strings.Repeat("multibyte-Ω\n", consoleFrameBytes)
	go func() {
		empty := relay.Write(0, nil)
		first := relay.Write(1, []byte(large))
		second := relay.Write(0, []byte("stdout marker"))
		done <- errors.Join(empty, first, second, connection.Close())
	}()
	var stdout, stderr bytes.Buffer
	require.NoError(t, copyConsoleFrames(input, [2]io.Writer{&stdout, &stderr}))
	err := <-done
	joined = true
	require.NoError(t, err)
	assert.Equal(t, "stdout marker", stdout.String())
	assert.Equal(t, large, stderr.String())
}

func TestConsoleFramesRejectMalformedInput(t *testing.T) {
	oversized := testConsoleFrame(0, []byte("x"))
	binary.LittleEndian.PutUint32(oversized[1:], consoleFrameBytes+1)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "short header", data: []byte{0, 1}},
		{name: "short payload", data: testConsoleFrame(0, []byte("abc"))[:7]},
		{name: "unknown stream", data: testConsoleFrame(2, []byte("x"))},
		{name: "empty frame", data: testConsoleFrame(0, nil)},
		{name: "oversized", data: oversized},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			require.Error(t, copyConsoleFrames(bytes.NewReader(test.data), [2]io.Writer{&stdout, &stderr}))
			assert.Empty(t, stdout.String())
			assert.Empty(t, stderr.String())
		})
	}
	var output bytes.Buffer
	require.NoError(t, copyConsoleFrames(bytes.NewReader(nil), [2]io.Writer{&output, &output}))
}

type fragmentedConsoleWriter struct {
	bytes.Buffer
	maximum int
}

func (w *fragmentedConsoleWriter) Write(data []byte) (int, error) {
	return w.Buffer.Write(data[:min(w.maximum, len(data))])
}

func TestConsoleWritesPreservePartialProgress(t *testing.T) {
	output := &fragmentedConsoleWriter{maximum: 2}
	require.NoError(t, writeConsoleBytes(output, []byte("complete output")))
	assert.Equal(t, "complete output", output.String())
	stalled := &fragmentedConsoleWriter{}
	require.ErrorIs(t, writeConsoleBytes(stalled, []byte("output")), io.ErrShortWrite)
}

func TestConsoleFramesPreserveOutputBeforeARealWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read-only-console")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	readOnly, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readOnly.Close() })
	frames := append(testConsoleFrame(0, []byte("written first")), testConsoleFrame(1, []byte("cannot write"))...)
	var stdout bytes.Buffer
	require.Error(t, copyConsoleFrames(bytes.NewReader(frames), [2]io.Writer{&stdout, readOnly}))
	assert.Equal(t, "written first", stdout.String())
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, contents)
}

func TestConsoleFailureReports(t *testing.T) {
	err := readConsoleFailure(strings.NewReader(fmt.Sprintf(`{"message":"access refused","code":%d}`, syscall.EACCES)))
	require.ErrorIs(t, err, syscall.EACCES)
	require.ErrorContains(t, err, "access refused")
	require.ErrorIs(t, readConsoleFailure(strings.NewReader(`{"message":"closed","closed":true}`)), os.ErrClosed)
	require.NoError(t, readConsoleFailure(strings.NewReader("")))
	for _, data := range []string{
		"{unfinished", `{}`, `{"message":""}`, `{"message":"invalid","code":-1}`, `{"message":"invalid","code":18446744073709551616}`,
		strings.Repeat("x", 4097),
	} {
		require.Error(t, readConsoleFailure(strings.NewReader(data)))
	}
}

func blockedConsoleRelay(t *testing.T, ctx context.Context, cancel context.CancelFunc, clock quartz.Clock) (*consoleRelay, procutil.ProcessIdentity) {
	t.Helper()
	reader, console, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = console.Close() })
	fillConsolePipe(t, reader, console)
	relay, err := startConsoleRelay(ctx, cancel, [2]*os.File{console, console}, clock)
	require.NoError(t, err)
	t.Cleanup(func() { relay.Interrupt(); _ = relay.Close() })
	identity, identified := procutil.IdentifyProcess(relay.owner.PID())
	require.True(t, identified)
	require.NoError(t, relay.Write(0, []byte("the full console cannot consume this frame")))
	return relay, identity
}

func TestConsoleRelayEndsWhenItsParentLifetimePipeCloses(t *testing.T) {
	waitCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(waitCtx)
	defer cancel()
	relay, identity := blockedConsoleRelay(t, ctx, cancel, quartz.NewReal())
	require.NoError(t, relay.lifetime.Close())
	select {
	case <-relay.exited:
	case <-waitCtx.Done():
		t.Fatal("the helper survived its parent lifetime pipe")
	}
	err := relay.Close()
	require.ErrorContains(t, err, "ended before delivery finished")
	assert.Equal(t, err, relay.Close(), "repeated close retains its outcome")
	assert.False(t, identity.Runs(), "the exact helper creation identity must have exited")
}

func TestConsoleRelayDrainDeadlineEndsBlockedDelivery(t *testing.T) {
	waitCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(waitCtx)
	defer cancel()
	var applicationCancelled atomic.Bool
	clock := testutil.NewQuartzMock(t)
	relay, identity := blockedConsoleRelay(t, ctx, func() { applicationCancelled.Store(true); cancel() }, clock)
	created := clock.Trap().NewTimer(consoleDrainTag)
	defer created.Close()
	began := make(chan struct{})
	go func() { relay.BeginDrain(); close(began) }()
	call := created.MustWait(waitCtx)
	call.MustRelease(waitCtx)
	require.Equal(t, time.Second, call.Duration)
	select {
	case <-began:
	case <-waitCtx.Done():
		t.Fatal("console drain did not start")
	}
	done := make(chan error, 1)
	go func() { done <- relay.Close() }()
	clock.Advance(time.Second).MustWait(waitCtx)
	select {
	case err := <-done:
		require.NoError(t, err, "optional console cutoff must not fail a completed attempt")
	case <-waitCtx.Done():
		t.Fatal("the console drain deadline did not finish the relay")
	}
	assert.False(t, applicationCancelled.Load())
	assert.False(t, identity.Runs())
}

func TestOutputCaptureRejectsClosedConsolesBeforeDispatch(t *testing.T) {
	console, err := os.Create(filepath.Join(t.TempDir(), "closed-console"))
	require.NoError(t, err)
	require.NoError(t, console.Close())
	stdout := os.Stdout
	os.Stdout = console
	t.Cleanup(func() { os.Stdout = stdout })
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	var attempt bytes.Buffer
	capture, err := startOutputCapture(ctx, &attempt, cancel, lifecycle.ExecutionForeground)
	require.ErrorIs(t, err, os.ErrClosed)
	assert.Nil(t, capture)
	assert.Empty(t, attempt.String())
	require.NoError(t, ctx.Err())
}

func TestConsoleRelayCloseJoinsStartedCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		clock := testutil.NewQuartzMock(t)
		trap := clock.Trap().Now()
		defer trap.Close()
		connection, relay := net.Pipe()
		lifetime, err := os.CreateTemp(t.TempDir(), "lifetime")
		require.NoError(t, err)
		r := &consoleRelay{connection: connection, relay: relay, lifetime: lifetime, cancel: func() {}, clock: clock, relayDone: make(chan error, 1), reportDone: make(chan error, 1), exited: make(chan struct{})}
		r.phase.Store(consoleRelayInterrupted)
		r.relayDone <- nil
		r.reportDone <- nil
		close(r.exited)
		r.watchCancellation(ctx, func() {})
		cancel()
		held := trap.MustWait(t.Context())
		closed := make(chan error, 1)
		var returned atomic.Bool
		go func() { closed <- r.Close(); returned.Store(true) }()
		synctest.Wait()
		assert.False(t, returned.Load(), "Close must join the started callback")
		held.MustRelease(t.Context())
		synctest.Wait()
		require.NoError(t, <-closed)
		require.NoError(t, r.Close())
		select {
		case <-r.cancellationDone:
		default:
			t.Fatal("Close returned before callback completion")
		}
	})
}

func TestConsoleRelayExitSignalJoinsWaiterCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		connection, transport := net.Pipe()
		lifetime, err := os.CreateTemp(t.TempDir(), "lifetime")
		require.NoError(t, err)
		r := &consoleRelay{connection: connection, relay: transport, lifetime: lifetime, cancel: func() {}, clock: testutil.NewQuartzMock(t), relayDone: make(chan error, 1), reportDone: make(chan error, 1), exited: make(chan struct{})}
		r.relayDone <- nil
		r.reportDone <- nil
		r.watchCancellation(t.Context(), func() {})
		entered, release := make(chan struct{}), make(chan struct{})
		go r.waitForHelper(func() { close(entered); <-release })
		<-entered
		synctest.Wait()
		select {
		case <-r.exited:
			t.Error("helper completion was signalled before waiter cleanup returned")
		default:
		}
		close(release)
		synctest.Wait()
		<-r.exited
		require.ErrorContains(t, r.Close(), "exited unexpectedly")
	})
}

func TestConsoleRelayCrashDuringDrainRemainsFailure(t *testing.T) {
	waitCtx, stopWaiting := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopWaiting()
	ctx, cancel := context.WithCancel(waitCtx)
	defer cancel()
	var consoles [2]*os.File
	for index := range consoles {
		file, err := os.CreateTemp(t.TempDir(), "console")
		require.NoError(t, err)
		consoles[index] = file
		t.Cleanup(func() { require.NoError(t, file.Close()) })
	}
	relay, err := startConsoleRelay(ctx, cancel, consoles, quartz.NewReal())
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	relay.phase.Store(consoleRelayDraining)
	require.NoError(t, relay.owner.Terminate())
	select {
	case <-relay.exited:
	case <-waitCtx.Done():
		t.Fatal("the helper did not finish after termination")
	}
	require.ErrorContains(t, relay.Close(), "exited unexpectedly")
}
