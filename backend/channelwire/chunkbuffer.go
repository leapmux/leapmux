package channelwire

// ChunkBuffer accumulates one logical channel message's decrypted plaintext chunks
// during reassembly. It is the raw accumulate-and-cap primitive shared by the two Go
// receivers that reassemble chunked channel messages: the tunnel client
// (tunnel.Channel) and the worker session (internal/worker/channel.reassembler).
//
// It is DELIBERATELY only the primitive, not the reassembly state machine. Each
// receiver wraps it in its own machine with different cap, reap, and locking
// semantics -- the tunnel client reaps tombstones through its handler registry under
// a lock; the worker counts tombstones toward its cap, reaps on the terminal chunk,
// and runs single-goroutine with no registry. Read those types before changing this
// one: the struct is shared, but the machines around it encode each receiver's own
// trust model. (The Hub's channelmgr.chunkTracker tracks only an estimated size --
// it never decrypts -- so it does not use this type.)
type ChunkBuffer struct {
	Parts [][]byte
	Total int
	// Poisoned marks a correlation id whose message already breached a limit and was
	// errored to its consumer. Its remaining chunks are dropped rather than
	// re-buffered; the entry itself stays as a zero-byte tombstone so they are
	// recognised. How and when the tombstone is reaped -- terminal chunk vs handler
	// unregistration -- stays with each receiver.
	Poisoned bool
}

// Poison marks the buffer failed and releases the parts it had accumulated. The
// entry stays as a zero-byte tombstone so later chunks for the id are recognised and
// dropped; the reap stays with each caller.
func (b *ChunkBuffer) Poison() {
	b.Poisoned = true
	b.Parts = nil
	b.Total = 0
}

// AppendChunk appends chunk to the buffer, adds its length to the running total, and
// reports the new total plus whether it now breaches limit. It is the
// accumulate-and-check step both the MORE and final-chunk branches of a receiver's
// reassembly share; the divergent response to a breach -- tombstone mid-sequence,
// reap on the terminal chunk -- stays with each caller.
func (b *ChunkBuffer) AppendChunk(chunk []byte, limit int) (total int, breached bool) {
	b.Parts = append(b.Parts, chunk)
	b.Total += len(chunk)
	return b.Total, b.Total > limit
}

// Join concatenates the buffer's ordered chunk parts into one pre-sized slice.
// It is the shared tail of both Go receivers' final-chunk delivery -- the worker
// session's reassembler joins buf directly while it is still live, and the
// tunnel client carries the *ChunkBuffer pointer out of the locked region and
// joins outside the lock (the pointer survives the buffer-map delete, so the
// parts it reads are the same slices AppendChunk filled). Promoted from a free
// function so the join reads b's own fields rather than callers reaching into
// b.Parts and b.Total, and so a rename or wrapping of those fields has one site
// to update.
func (b *ChunkBuffer) Join() []byte {
	out := make([]byte, 0, b.Total)
	for _, part := range b.Parts {
		out = append(out, part...)
	}
	return out
}

// CountByState tallies a reassembly buffer map into its live (accumulating) and
// tombstoned (Poisoned) counts. It is the shared count both Go receivers derive
// -- the tunnel client (tunnel.Channel.liveReassemblyLocked) takes only the live
// count because its tombstones are reaped by handler unregistration; the worker
// session (internal/worker/channel.reassembler.counts) takes both because it
// bounds tombstones itself. Derived by scanning rather than kept as running
// totals so neither count can drift from the map it describes; the map is
// bounded (live plus tombstones, each capped at the receiver's max-incomplete),
// so the scan is trivial.
func CountByState(buffers map[uint64]*ChunkBuffer) (live, tombstones int) {
	for _, buf := range buffers {
		if buf.Poisoned {
			tombstones++
		} else {
			live++
		}
	}
	return live, tombstones
}
