// Package tunnelflow is the single source of truth for the tunnel data path's
// end-to-end flow-control window sizes and the window mechanism that consumes
// them. The client half lives in the public tunnel package (Conn's send window
// and read buffer) and the worker half lives in internal/worker/service (the
// per-conn write-seq gate and read-send credit), and the two packages do not
// import each other -- so before these constants were centralized here their
// correctness relationships were coupled by prose comments alone. A retune of
// one silently broke the others.
//
// Three invariants must hold or tunnelled traffic breaks. They are enforced at
// compile time below, so a retune that violates one fails the build (pointing at
// the offending relationship) rather than shipping a latent hang or starvation:
//
//   - WriteWindowFrames <= MaxWriteSeqLookahead: the worker's waitWriteTurn NAKs
//     any SendTunnelData whose seq is more than MaxWriteSeqLookahead ahead of its
//     gate. A correct client has at most WriteWindowFrames unacknowledged frames
//     in flight, so its seqs span at most that far ahead; keeping the lookahead
//     at or above the window guarantees a legitimate in-window frame is never
//     rejected.
//   - InitialReadWindow < ReadBufFrames: the worker streams at most
//     InitialReadWindow inbound frames before it needs a client credit grant, and
//     the client buffers them in a channel of ReadBufFrames capacity. Keeping the
//     window strictly below the buffer means the buffer always holds the whole
//     in-flight window, so the shared channel's recvLoop never blocks delivering
//     to a slow tunnel consumer -- the starvation the read-credit mechanism exists
//     to prevent.
//   - ReadCreditBatch < InitialReadWindow: the client returns credit in
//     ReadCreditBatch-sized grants as it drains. Keeping the batch strictly below
//     the window returns credit before the worker exhausts it, so a steadily
//     draining consumer never stalls waiting for a grant that has not been sent.
package tunnelflow

// Client-side (backend/tunnel) window sizes.
const (
	// WriteWindowFrames bounds how many SendTunnelData frames the client may have
	// sent but not yet had acknowledged by the worker -- the client's half of the
	// write flow control. Once the window is full, Write blocks until the worker
	// acks an earlier frame, backpressuring the local source through io.Copy
	// instead of letting the worker accumulate unbounded in-flight goroutines and
	// buffered payloads.
	//
	// Sizing is a throughput-vs-memory tradeoff, not an arbitrary bound: a window
	// caps upload at window/RTT regardless of link bandwidth (the bandwidth-delay
	// product), so 64 x MaxChunkBytes = 2 MiB in flight sustains ~20 MB/s at a
	// 100 ms cross-region hub RTT, at a worst case of 2 MiB pinned per uploading
	// conn. Raise both this and MaxWriteSeqLookahead together if tunnels must
	// saturate higher-BDP links -- the compile-time checks below catch a retune that
	// breaks a relationship.
	WriteWindowFrames = 64

	// ReadBufFrames is the capacity of the client Conn's inbound frame buffer. It
	// must exceed InitialReadWindow (enforced below) so the client can always hold
	// the worker's whole in-flight read window and the shared channel's recvLoop
	// is never blocked by a slow tunnel consumer.
	ReadBufFrames = 256

	// ReadCreditBatch is how many consumed inbound frames accumulate before the
	// client sends a GrantTunnelReadCredit top-up. Batching avoids a credit RPC
	// per frame; keeping it below InitialReadWindow (enforced below) returns credit
	// before the worker exhausts its window so a steady consumer never stalls.
	ReadCreditBatch = 16

	// MaxChunkBytes bounds the payload of a single tunnel data frame in EITHER
	// direction: a client's SendTunnelData and a worker's inbound TunnelConnEvent
	// alike. Conn.Write splits a larger buffer across frames, which is what makes
	// each window a BYTE bound -- WriteWindowFrames * MaxChunkBytes upstream,
	// InitialReadWindow * MaxChunkBytes downstream -- rather than merely a
	// frame-count bound.
	//
	// Without the split the in-flight byte ceiling is a property of the CALLER's
	// buffer size, not of the protocol: it happens to hold today only because every
	// caller is an io.Copy with a 32 KiB buffer. A writer that hands over a large
	// buffer in one call (a bufio.Writer, bytes.Buffer.WriteTo, an http.Transport
	// flushing a big body) would pin up to WriteWindowFrames * DefaultMaxMessageSize
	// (64 * 17 MiB, over 1 GiB) on the worker, and a single Write above the channel's
	// inner-message limit would fail outright -- which net.Conn.Write, whose
	// contract accepts a buffer of any size, must never do.
	//
	// The worker's target read buffer is DERIVED from this constant
	// (tunnelReadBufSize in internal/worker/service), not merely equal to it by
	// coincidence: the worker sends each target Read as a single frame, so a read
	// buffer larger than this bound would emit frames its own peer rejects. Deriving
	// it -- rather than asserting the relationship the way the invariants below do --
	// is what keeps that claim true through a retune, and it must be derived on the
	// worker's side because this package is imported BY internal/worker/service and
	// so cannot see its constants.
	//
	// So the two directions frame at the same granularity -- which is why ONE constant
	// bounds both, and why each RECEIVER enforces it on receipt rather than trusting
	// the sender to have split: a peer's split is a convention, and a receiver that
	// assumes it has a byte ceiling only as long as the peer is well-behaved.
	MaxChunkBytes = 32 * 1024
)

// Worker-side (backend/internal/worker/service) window sizes.
const (
	// MaxWriteSeqLookahead bounds how far ahead of the worker's per-conn write gate
	// a SendTunnelData seq may legitimately be. A correct client's in-flight seqs
	// span at most WriteWindowFrames; this sits at or above that (enforced below)
	// so a legitimate frame is never NAKed, while a seq beyond it is a protocol
	// violation the gate rejects instead of parking a handler goroutine forever.
	MaxWriteSeqLookahead = 256

	// InitialReadWindow is the read-send credit a worker conn starts with: the
	// worker may stream this many inbound frames before it needs a client grant.
	// It must stay below ReadBufFrames (enforced below) so the client can buffer
	// the whole window, and above ReadCreditBatch so grants arrive before it is
	// exhausted.
	//
	// It is the download direction's bandwidth-delay product, mirroring
	// WriteWindowFrames: 128 x the worker's MaxChunkBytes read chunk = 4 MiB in flight,
	// ~40 MB/s at a 100 ms RTT, bounded by the client's ReadBufFrames buffer.
	InitialReadWindow = 128
)

// Compile-time enforcement of the flow-control invariants. Each expression is a
// non-negative margin, and uint(negative) is a compile-time error, so a retune
// that violates an invariant stops the build here -- with the offending line's
// comment naming the relationship that broke -- rather than shipping a hang or a
// starvation bug. The constants are untyped so these expressions evaluate as
// exact integers regardless of the typed aliases the consumers declare.
const (
	_ = uint(MaxWriteSeqLookahead - WriteWindowFrames) // WriteWindowFrames <= MaxWriteSeqLookahead
	_ = uint(ReadBufFrames - InitialReadWindow - 1)    // InitialReadWindow < ReadBufFrames
	_ = uint(InitialReadWindow - ReadCreditBatch - 1)  // ReadCreditBatch < InitialReadWindow
)
