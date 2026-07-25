package agent

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetStdoutLimitsForTest(t *testing.T) {
	t.Helper()
	stdoutMu.Lock()
	prevTok := stdoutMaxTokenSize.Load()
	prevCfg := stdoutConfiguredMax
	prevOpen := stdoutOpenChannels
	prevNeg := stdoutNegotiatedMax
	stdoutOpenChannels = map[string]struct{}{}
	stdoutNegotiatedMax = 0
	stdoutConfiguredMax = channelwire.MaxMessageSize
	stdoutMaxTokenSize.Store(int64(channelwire.MaxMessageSize))
	stdoutMu.Unlock()
	t.Cleanup(func() {
		stdoutMu.Lock()
		stdoutConfiguredMax = prevCfg
		stdoutOpenChannels = prevOpen
		stdoutNegotiatedMax = prevNeg
		stdoutMaxTokenSize.Store(prevTok)
		stdoutMu.Unlock()
	})
}

func TestConfigureMaxMessageSize_RaisesStdoutScannerCeiling(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const small = 1024
	const raised = 4096
	ConfigureMaxMessageSize(small)
	assert.Equal(t, small, liveStdoutMaxTokenSize())
	assert.Equal(t, small, stdoutConfiguredMax)

	oversized := strings.Repeat("x", small+512)
	scanner := newStdoutScanner(strings.NewReader(oversized + "\n"))
	require.False(t, scanner.Scan(), "line above the configured ceiling must not scan")
	require.Error(t, scanner.Err())

	ConfigureMaxMessageSize(raised)
	assert.Equal(t, raised, liveStdoutMaxTokenSize())
	scanner = newStdoutScanner(strings.NewReader(oversized + "\n"))
	require.True(t, scanner.Scan(), "raising max_message_size must admit the same line: %v", scanner.Err())
	assert.Len(t, scanner.Bytes(), len(oversized))

	ConfigureMaxMessageSize(0)
	assert.Equal(t, channelwire.MaxMessageSize, liveStdoutMaxTokenSize(),
		"0 must resolve back to the protocol default")
}

func TestObserveNegotiatedMaxMessageSize_RefcountTracksOpenChannels(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const worker = 4 << 20
	const negotiated = 1 << 20
	ConfigureMaxMessageSize(worker)
	ObserveNegotiatedMaxMessageSize("ch-a", negotiated)
	assert.Equal(t, negotiated, liveStdoutMaxTokenSize())
	assert.Equal(t, worker, stdoutConfiguredMax, "configured ceiling must stay at the worker knob")

	oversized := strings.Repeat("x", negotiated+512)
	scanner := newStdoutScanner(strings.NewReader(oversized + "\n"))
	require.False(t, scanner.Scan(), "line above the negotiated ceiling must not scan")

	// A second open on the same Hub↔Worker pair carries the same budget.
	ObserveNegotiatedMaxMessageSize("ch-b", negotiated)
	assert.Equal(t, negotiated, liveStdoutMaxTokenSize())

	ReleaseNegotiatedMaxMessageSize("ch-a")
	assert.Equal(t, negotiated, liveStdoutMaxTokenSize(),
		"releasing one channel must keep the negotiated ceiling while others remain open")

	ReleaseNegotiatedMaxMessageSize("ch-b")
	assert.Equal(t, worker, liveStdoutMaxTokenSize(),
		"releasing the last channel must restore the configured ceiling")
}

func TestObserveNegotiatedMaxMessageSize_IdempotentForSameChannel(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const worker = 4 << 20
	const negotiated = 1 << 20
	ConfigureMaxMessageSize(worker)
	ObserveNegotiatedMaxMessageSize("ch-1", negotiated)
	ObserveNegotiatedMaxMessageSize("ch-1", negotiated)
	stdoutMu.Lock()
	assert.Len(t, stdoutOpenChannels, 1)
	stdoutMu.Unlock()

	ReleaseNegotiatedMaxMessageSize("ch-1")
	assert.Equal(t, worker, liveStdoutMaxTokenSize(),
		"a duplicate Observe must not leave a stuck refcount after one Release")
}

func TestObserveNegotiatedMaxMessageSize_LiveScannerHonorsLaterShrink(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const high = 8 << 10
	const low = 2 << 10
	ConfigureMaxMessageSize(high)

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})
	scanner := newStdoutScanner(pr)
	ObserveNegotiatedMaxMessageSize("ch-live", low)

	go func() {
		_, _ = pw.Write([]byte(strings.Repeat("y", low+64) + "\n"))
		_ = pw.Close()
	}()

	require.False(t, scanner.Scan(), "live scanner must honor a later Observe shrink")
	require.ErrorIs(t, scanner.Err(), bufio.ErrTooLong)
}

func TestNewStdoutScanner_MultiLineBufferBelowCeilingScans(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const max = 64
	ConfigureMaxMessageSize(max)
	// Many short lines whose combined bytes exceed max must still scan —
	// ErrTooLong applies per token / incomplete line, not to the whole
	// buffer before ScanLines runs.
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteString(strings.Repeat("a", 12))
		b.WriteByte('\n')
	}
	require.Greater(t, b.Len(), max)

	scanner := newStdoutScanner(strings.NewReader(b.String()))
	require.True(t, scanner.Scan(), "first short line must scan: %v", scanner.Err())
	assert.Equal(t, strings.Repeat("a", 12), scanner.Text())
	require.True(t, scanner.Scan())
	assert.Equal(t, strings.Repeat("a", 12), scanner.Text())
}

func TestReleaseAllNegotiatedMaxMessageSizes_ClearsEveryBudget(t *testing.T) {
	resetStdoutLimitsForTest(t)

	const worker = 4 << 20
	const negotiated = 1 << 20
	ConfigureMaxMessageSize(worker)
	ObserveNegotiatedMaxMessageSize("ch-a", negotiated)
	ObserveNegotiatedMaxMessageSize("ch-b", negotiated)
	assert.Equal(t, negotiated, liveStdoutMaxTokenSize())

	ReleaseAllNegotiatedMaxMessageSizes()
	assert.Equal(t, worker, liveStdoutMaxTokenSize())
	stdoutMu.Lock()
	assert.Empty(t, stdoutOpenChannels)
	assert.Zero(t, stdoutNegotiatedMax)
	stdoutMu.Unlock()
}
