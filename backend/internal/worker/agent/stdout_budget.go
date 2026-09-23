package agent

// stdout_budget.go holds the ceiling on one line of an agent's stdout: the
// worker's configured payload budget, lowered while a channel negotiated a smaller
// one. Every provider's scanner reads it.

import (
	"bufio"
	"io"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
)

// stdoutConfiguredMax is the worker's configured application payload budget
// (the raise-floor for the live scanner ceiling when no channel is open).
//
// A Hub↔Worker pair always negotiates one effective budget
// (min(hub, worker)) for every channel open, so we keep a refcount of open
// channel ids plus a single negotiated value — not a per-channel budget map.
// While any channel is open the live ceiling is min(configured, negotiated);
// when the last channel closes it rises back to configured.
// stdoutMaxTokenSize is the live scanner token ceiling read by the
// ScanLines wrapper without holding stdoutMu.
var (
	stdoutMu            sync.Mutex
	stdoutConfiguredMax = contracts.MaxMessageSize
	stdoutOpenChannels  = map[string]struct{}{}
	stdoutNegotiatedMax int // meaningful only while len(stdoutOpenChannels) > 0
	stdoutMaxTokenSize  atomic.Int64
)

func init() {
	stdoutMaxTokenSize.Store(int64(contracts.MaxMessageSize))
}

func recomputeStdoutMaxLocked() {
	min := stdoutConfiguredMax
	if len(stdoutOpenChannels) > 0 && stdoutNegotiatedMax < min {
		min = stdoutNegotiatedMax
	}
	stdoutMaxTokenSize.Store(int64(min))
}

// ConfigureMaxMessageSize sets the worker's configured application payload
// budget (0 resolves to the protocol default) and recomputes the live
// scanner ceiling against any open negotiated budget. Bootstrap calls
// this once before agents start.
func ConfigureMaxMessageSize(n int) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutConfiguredMax = channelwire.ResolveMaxMessageSize(n)
	recomputeStdoutMaxLocked()
}

// ObserveNegotiatedMaxMessageSize records that channelID is open at the
// Hub↔Worker negotiated payload budget and sets the live scanner ceiling
// to min(configured, negotiated). Call on successful HandleOpen. Every
// open on this worker pair carries the same effective budget; the
// channel id is tracked only so Release can drop the refcount.
func ObserveNegotiatedMaxMessageSize(channelID string, n int) {
	n = channelwire.ResolveMaxMessageSize(n)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutOpenChannels[channelID] = struct{}{}
	stdoutNegotiatedMax = n
	recomputeStdoutMaxLocked()
}

// ReleaseNegotiatedMaxMessageSize drops channelID from the open set (on
// HandleClose). When the last channel closes, the live ceiling rises back
// to the worker configured budget — including Hub reject-after-accept
// paths that tear the worker session down.
func ReleaseNegotiatedMaxMessageSize(channelID string) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	delete(stdoutOpenChannels, channelID)
	if len(stdoutOpenChannels) == 0 {
		stdoutNegotiatedMax = 0
	}
	recomputeStdoutMaxLocked()
}

// ReleaseAllNegotiatedMaxMessageSizes clears every open-channel ref (CloseAll)
// and restores the live ceiling to the configured budget.
func ReleaseAllNegotiatedMaxMessageSizes() {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutOpenChannels = map[string]struct{}{}
	stdoutNegotiatedMax = 0
	recomputeStdoutMaxLocked()
}

// LiveMaxMessageSize returns the current scanner token ceiling: the configured
// budget, lowered to the negotiated one while any channel is open. It reads an
// atomic, so a renegotiation can change the answer between two calls.
func LiveMaxMessageSize() int {
	return int(stdoutMaxTokenSize.Load())
}

// ConfiguredMaxMessageSize returns the worker's configured payload budget, the
// ceiling the live value rises back to when the last channel closes. A scanner
// sized at creation takes this value, because a later negotiation can raise the
// live ceiling back up to it but never above it.
func ConfiguredMaxMessageSize() int {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	return stdoutConfiguredMax
}

// StdoutScannerStartBuf is the scanner's initial buffer. It grows on
// demand up to the ceiling below, so this only decides how many
// reallocations a long line costs.
const StdoutScannerStartBuf = 1024 * 1024

// NewStdoutScanner returns the line scanner every agent provider reads
// its subprocess stdout with.
//
// The Buffer capacity ceiling is the worker's configured payload budget
// (an upper bound that never shrinks below what operators allow). The
// live negotiated min is enforced by the SplitFunc on every Scan so a
// later Observe can shrink (or a Release can raise) without recreating
// already-running scanners — bufio.Scanner freezes Buffer's maxTokenSize
// at creation time.
//
// Shared rather than repeated per provider because it is scanner
// plumbing bound to a shared limit, with nothing provider-specific in
// it. What IS provider-specific -- how each one interprets the lines --
// stays in that provider, which is where the read loop the caller hands
// this to lives.
func NewStdoutScanner(stdout io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(stdout)
	stdoutMu.Lock()
	configured := stdoutConfiguredMax
	stdoutMu.Unlock()
	start := StdoutScannerStartBuf
	if start > configured {
		start = configured
	}
	scanner.Buffer(make([]byte, 0, start), configured)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		max := LiveMaxMessageSize()
		if max < 1 {
			max = 1
		}
		// ScanLines first so a buffer of many short lines totaling > max
		// still yields the first short token. ErrTooLong only when a single
		// line (complete token, or incomplete line with no newline yet)
		// exceeds the live ceiling — otherwise a later Observe shrink would
		// still admit an oversized in-progress line that Buffer's configured
		// max would allow.
		advance, token, err = bufio.ScanLines(data, atEOF)
		if err != nil {
			return 0, nil, err
		}
		if token != nil && len(token) > max {
			return 0, nil, bufio.ErrTooLong
		}
		if advance == 0 && !atEOF && len(data) > max {
			return 0, nil, bufio.ErrTooLong
		}
		return advance, token, nil
	})
	return scanner
}
