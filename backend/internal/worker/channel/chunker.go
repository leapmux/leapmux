package channel

import "github.com/leapmux/leapmux/channelwire"

// The per-message chunk buffer (accumulate, cap, poison) is the shared
// channelwire.ChunkBuffer -- byte-identical to the tunnel client's use of it. Only
// the state machine below (cap counted toward tombstones, terminal-chunk reaping,
// single-goroutine access, no handler registry) is this receiver's own.

// reassembler owns one channel session's chunk-reassembly state machine:
// buffering MORE chunks per correlation id, bounding the number of in-flight
// sequences and the reassembled size, poisoning a sequence that breaches the
// size limit, and reaping tombstones on their terminal chunk. It decides;
// acting on the decision (logging with channel context, erroring the peer via
// the session's sender) stays with the caller, so this type is testable
// without a live Noise session.
//
// It is deliberately lock-free: Manager.HandleMessage is the only caller and
// runs on the session's sole receive goroutine (decryption is sequential
// because the receive cipher state tracks a nonce counter), so unlike the
// tunnel client's reassembly (tunnel.Channel.reassemble, guarded by ch.mu)
// there is no cross-goroutine access to guard.
//
// Live buffers and tombstones are each capped at maxIncomplete. Live buffers
// are what the cap protects against (each can hold up to maxMessageSize), so
// they are what the live cap bounds. A cap-exceeded id is TOMBSTONED rather
// than refused without a trace: erroring without recording it (as this once
// did) let every later MORE chunk for the id re-enter the cap branch and
// re-fire sendError -- one encrypt+send goroutine per chunk, storming the
// channel's sole receive goroutine exactly the way the tunnel client's
// tombstone exists to prevent -- and left the id's terminal chunk to fall
// through to the no-buffer deliver path and be dispatched as a bogus
// single-chunk message. Tombstoning fixes both (one error; the terminal chunk
// is reaped, not delivered). Tombstones are bounded separately because this
// receiver, unlike the client, has no handler registry to reap them: without a
// bound a peer withholding terminals could grow the map without limit. The
// worst case is still a self-DoS strictly cheaper than the live sequences the
// same peer could pin the budget with instead: at most maxIncomplete live
// buffers (real memory) plus maxIncomplete zero-byte tombstones.
type reassembler struct {
	// buffers maps correlation id -> in-progress (or tombstoned) sequence.
	buffers map[uint64]*channelwire.ChunkBuffer
	// maxMessageSize bounds one reassembled message's total bytes;
	// maxIncomplete bounds how many sequences (live or tombstoned) may be in
	// flight at once.
	maxMessageSize int
	maxIncomplete  int
}

func newReassembler(maxMessageSize, maxIncomplete int) *reassembler {
	return &reassembler{
		buffers:        make(map[uint64]*channelwire.ChunkBuffer),
		maxMessageSize: maxMessageSize,
		maxIncomplete:  maxIncomplete,
	}
}

// reassemblyAction is what accept tells the caller to do with the chunk it was
// handed.
type reassemblyAction int

const (
	// reassemblyDeliver: the message is complete -- dispatch outcome.plaintext.
	reassemblyDeliver reassemblyAction = iota
	// reassemblyBuffered: the chunk was buffered; more are expected.
	reassemblyBuffered
	// reassemblyDropPoisoned: the id already breached a limit and was errored
	// once; this chunk was dropped without buffering a byte.
	reassemblyDropPoisoned
	// reassemblyTooManyIncomplete: the live cap is full, so the id was tombstoned
	// rather than buffered. The caller errors the id ONCE with
	// RESOURCE_EXHAUSTED; its later chunks surface as reassemblyDropPoisoned and
	// its terminal chunk reaps the tombstone. The tombstone counts toward the
	// tombstone cap, not the live cap.
	reassemblyTooManyIncomplete
	// reassemblyDropCapped: the live cap AND the tombstone cap are both full, so
	// the chunk was dropped without an error or a tombstone. A peer that has
	// saturated both budgets gets no more chunked service until something
	// completes or is reaped; the alternative (a fresh tombstone per chunk) is
	// itself the unbounded-growth storm the tombstone cap exists to prevent. A
	// terminal (non-MORE) chunk with no buffer is dropped the same way while
	// both budgets are full: with no tombstone to reap it, delivering it would
	// dispatch a decrypted fragment as a bogus single-chunk message.
	reassemblyDropCapped
	// reassemblyTooLarge: the sequence breached maxMessageSize and was
	// poisoned (or, when the breach arrived on its terminal chunk, reaped
	// outright). The caller errors the id with RESOURCE_EXHAUSTED exactly
	// once -- the id's later chunks surface as reassemblyDropPoisoned.
	reassemblyTooLarge
)

// reassemblyOutcome is accept's decision plus the context the caller needs to
// act on it (dispatch, log, or error the peer).
type reassemblyOutcome struct {
	action reassemblyAction
	// plaintext is the complete message. Set for reassemblyDeliver only.
	plaintext []byte
	// chunked reports whether plaintext was reassembled from buffered chunks
	// rather than arriving whole. Set for reassemblyDeliver only.
	chunked bool
	// size is the sequence's accumulated byte count. Set for
	// reassemblyBuffered, reassemblyTooLarge, and reassemblyDeliver.
	size int
	// incomplete is the number of in-flight sequences that made the cap
	// refuse a new one. Set for reassemblyTooManyIncomplete only.
	incomplete int
}

// accept advances the state machine by one decrypted chunk for requestID and
// returns what the caller must do with it. `more` is the wire's MORE flag: a
// terminal (non-MORE) chunk completes -- or, for a poisoned id, reaps -- the
// sequence.
//
// An id already errored for breaching a limit has its remaining chunks dropped
// without re-buffering a byte, and its tombstone reaped on the terminal chunk.
//
// Deleting the buffer on breach (as this once did) let the very next MORE
// chunk find no entry, pass the max-incomplete check against a map the delete
// had just shrunk, allocate a fresh buffer and re-accumulate to the
// ceiling -- erroring, deleting and repeating for as long as the peer kept
// sending. Each cycle burns a full message budget plus an error goroutine on the worker's
// sole receive goroutine. Unlike the client's channel, this receiver's
// requests are PEER-initiated, so there is no live-handler check to bound it:
// the tombstone is the bound. It is also the same defect already fixed on the
// client side (see Channel.reassemble in the tunnel package), and this side
// faces the more hostile peer -- any channel holder, including a delegation
// bearer driving a prompt-injectable agent.
//
// The cap-exceeded path tombstones for the same reason (see the reassembler
// type doc): refusing without a trace re-fired sendError once per chunk and
// left the terminal chunk to be delivered as a bogus single-chunk message. The
// residual window -- when BOTH caps are full there is no tombstone to record
// the dropped id -- is closed at the terminal-chunk deliver path, which drops
// rather than delivers a no-buffer chunk while the budgets are saturated.
//
// The terminal-chunk reap below is the ONLY reaper here (the worker has no
// unregisterRequest; the session map is otherwise released at HandleClose), so
// a peer that never terminates a poisoned sequence leaves at most
// maxIncomplete ZERO-BYTE tombstones. That is no new capability: the same peer
// can already pin the budget with that many live incomplete sequences, and
// those cost real memory.
func (r *reassembler) accept(requestID uint64, chunk []byte, more bool) reassemblyOutcome {
	if buf, exists := r.buffers[requestID]; exists && buf.Poisoned {
		if !more {
			delete(r.buffers, requestID)
		}
		return reassemblyOutcome{action: reassemblyDropPoisoned}
	}

	if more {
		// More chunks to come -- buffer this one.
		buf, exists := r.buffers[requestID]
		if !exists {
			// New chunked sequence -- check the live cap. A full live cap
			// tombstones the id (see the reassembler type doc) rather than
			// refusing without a trace: the traceless refusal re-fired
			// sendError per chunk and let the terminal chunk be delivered as
			// a single-chunk message. Both caps full means the peer has
			// exhausted the budget; drop the chunk silently rather than
			// adding a tombstone that would itself grow without bound.
			live, tombstones := r.counts()
			if live >= r.maxIncomplete {
				if tombstones >= r.maxIncomplete {
					return reassemblyOutcome{action: reassemblyDropCapped}
				}
				r.buffers[requestID] = &channelwire.ChunkBuffer{Poisoned: true}
				return reassemblyOutcome{action: reassemblyTooManyIncomplete, incomplete: live}
			}
			buf = &channelwire.ChunkBuffer{}
			r.buffers[requestID] = buf
		}
		total, breached := buf.AppendChunk(chunk, r.maxMessageSize)
		if breached {
			r.poisonAndCap(requestID, buf)
			return reassemblyOutcome{action: reassemblyTooLarge, size: total}
		}
		return reassemblyOutcome{action: reassemblyBuffered, size: total}
	}

	// Terminal chunk (or single non-chunked message).
	buf, exists := r.buffers[requestID]
	if !exists {
		// No buffered prefix. Normally a genuine single-chunk message, delivered
		// as-is. But when both budgets are saturated this may instead be the
		// terminal chunk of a sequence whose earlier MORE chunks were
		// reassemblyDropCapped -- dropped without a tombstone, so there is
		// nothing here to route it to the poisoned branch, and delivering it
		// would dispatch a decrypted fragment as a bogus single-chunk message
		// (the very defect the tombstone reap closes for the live-cap-full case).
		//
		// The guard keys on the DERIVED live/tombstone counts rather than on
		// len(buffers): it fires only while BOTH budgets remain saturated, and an
		// abusive peer that filled them can drain one (completing one of its own
		// live sequences), at which point the terminal chunk is delivered. That
		// re-opened path grants no new capability -- the content is a peer-authored
		// plaintext the peer could have sent as an ordinary single-chunk message
		// anyway -- but deriving the counts (rather than relying on len == 2*max,
		// whose meaning depends on the live/tomb caps staying exact) keeps the
		// guard correct even if a future edit lets one budget slip its cap.
		live, tombstones := r.counts()
		if live >= r.maxIncomplete && tombstones >= r.maxIncomplete {
			return reassemblyOutcome{action: reassemblyDropCapped}
		}
		// Enforce the reassembled-message ceiling on the single-chunk path too,
		// the way the sibling tunnel receiver does (tunnel.Channel.reassemble):
		// wire.go states the ceiling is a fixed protocol constant EVERY receiver
		// enforces independently, and this was the one path that leaned on the
		// Hub's per-ciphertext cap instead. That cap (65535) bounds a decrypted
		// single chunk well below the reassembly ceiling today, so this never fires on
		// legitimate traffic, but enforcing it here makes the "every receiver
		// enforces independently" claim literally true and keeps an unbounded
		// plaintext off proto.Unmarshal if the upstream cap ever relaxes.
		if len(chunk) > r.maxMessageSize {
			return reassemblyOutcome{action: reassemblyTooLarge, size: len(chunk)}
		}
		return reassemblyOutcome{action: reassemblyDeliver, plaintext: chunk, size: len(chunk)}
	}
	// Concatenate buffered parts + this final chunk.
	total, breached := buf.AppendChunk(chunk, r.maxMessageSize)
	if breached {
		// The sequence's terminal chunk just arrived, so the id is finished:
		// poison to release the accumulated parts, then reap outright rather
		// than leaving a tombstone nothing would ever collect. A peer that
		// reuses the id starts a fresh sequence and is bounded afresh.
		buf.Poison()
		delete(r.buffers, requestID)
		return reassemblyOutcome{action: reassemblyTooLarge, size: total}
	}
	plaintext := buf.Join()
	delete(r.buffers, requestID)
	return reassemblyOutcome{action: reassemblyDeliver, plaintext: plaintext, chunked: true, size: len(plaintext)}
}

// poisonAndCap tombstones an in-progress buffer that just breached
// maxMessageSize and bounds the number of surviving tombstones, mirroring the
// frontend Reassembler.capTombstones (frontend/src/lib/channel.ts).
//
// Poisoning a LIVE buffer in place FREES its live-cap slot (counts() moves it
// from live to tombstone), so the live cap alone cannot bound the map here:
// without this cap a peer could loop {open a new chunked id -- admitted because
// live < maxIncomplete -- stream chunks past maxMessageSize to breach it into a
// permanent tombstone, never send the terminal chunk that would reap it} and
// grow buffers without limit for the channel's life, eventually tripping the
// terminal-chunk deliver guard (len(buffers) >= 2*maxIncomplete) so even the
// owner's own single-chunk RPCs are silently dropped. The new-sequence path
// bounds tombstones by REFUSING once the cap is full; this path cannot refuse a
// breach that already happened, so it evicts instead -- the tombstone with the
// SMALLEST id other than keep. Eviction never reaches keep (its peer is still
// actively sending, and its remaining chunks are what the fresh tombstone exists
// to swallow).
//
// Evicting the MINIMUM id is deterministic and heuristically best. Determinism
// matters so a test can assert WHICH id is evicted (non-deterministic map range
// can't be asserted against) and so the reassembler's behavior is reproducible.
// The heuristic is that the lowest correlation id is the oldest allocation,
// whose peer has had the longest to finish streaming and so is the least likely
// to still have chunks in flight that would re-enter accept as a fresh sequence
// after the eviction (a stray chunk for an evicted id is admitted afresh under
// the caps; min-id eviction minimizes -- but cannot eliminate, under bounded
// memory -- that re-accumulation, the tradeoff the type doc accepts).
func (r *reassembler) poisonAndCap(keep uint64, buf *channelwire.ChunkBuffer) {
	buf.Poison()
	for {
		if _, tombstones := r.counts(); tombstones <= r.maxIncomplete {
			return
		}
		var evictID uint64
		var found bool
		for id, b := range r.buffers {
			if b.Poisoned && id != keep && (!found || id < evictID) {
				evictID = id
				found = true
			}
		}
		// Only the just-poisoned keep is a tombstone (maxIncomplete is 0):
		// nothing else to evict, so stop rather than spin.
		if !found {
			return
		}
		delete(r.buffers, evictID)
	}
}

// counts returns the number of live (accumulating) and tombstoned (poisoned)
// buffers, via the shared channelwire.CountByState the tunnel client also uses.
// Derived rather than kept as running totals so neither can drift from the map
// it describes; the map is bounded (live plus tombstones, each capped at
// maxIncomplete), so the scan is trivial. The tunnel client counts live only
// (liveReassemblyLocked) because its tombstones are reaped by
// unregisterRequest; this receiver has no handler registry, so it bounds
// tombstones itself.
func (r *reassembler) counts() (live, tombstones int) {
	return channelwire.CountByState(r.buffers)
}
