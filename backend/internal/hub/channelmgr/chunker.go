package channelmgr

import (
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/channelwire"
)

const (
	// maxChunkCiphertext is the maximum ciphertext size for a single Noise
	// transport message. It mirrors channelwire.MaxCiphertextForChunk (65535 =
	// 65519 plaintext + 16 auth tag) and aliases the shared constant so a
	// transport-limit or AEAD-tag-size change is one edit in channelwire, not a
	// 65535/16 pair this package's tests would not catch diverging.
	maxChunkCiphertext = channelwire.MaxCiphertextForChunk
)

// chunkSequence tracks cumulative size for one in-flight chunked message.
type chunkSequence struct {
	estimatedPlaintext int
}

// channelChunkState tracks all in-flight chunk sequences for one channel+direction.
type channelChunkState struct {
	sequences    map[uint64]*chunkSequence // correlationID -> sequence
	inProgressID uint64                    // correlationID of current in-progress chunked send (0 = none)
}

// chunkTracker enforces message size limits and interleaving rules for
// chunked channel messages relayed through the Hub. The Hub cannot decrypt,
// but estimates plaintext size from ciphertext (ciphertext - 16 bytes auth tag).
type chunkTracker struct {
	mu sync.Mutex
	// defaultReassembledMax is the hub-configured reassembled-message
	// ceiling used until (and unless) SetChannelMax records a per-channel
	// negotiated ceiling.
	defaultReassembledMax int
	// channelMax holds per-channel reassembled ceilings that are stricter
	// than defaultReassembledMax (MaxReassembledMessageSize of the effective
	// payload budget). Equal-to-default negotiations skip the map; Track
	// falls back to defaultReassembledMax when a channel has no entry.
	channelMax map[string]int
	// channels maps channelID -> direction ("fe2w" or "w2fe") -> state
	channels map[string]map[string]*channelChunkState
}

// newChunkTracker builds the Hub-side tracker with the given default
// reassembled-message ceiling. Per-channel ceilings are applied later via
// SetChannelMax after open negotiation.
//
// There is deliberately no in-flight-sequence COUNT cap here. Track admits a new
// correlation id only while NO sequence is in progress on that channel+direction,
// so at most ONE chunked sequence is ever in flight per channel+direction. That
// interleaving rule is strictly stronger than any count cap of one or more, and
// subsumes it. The Worker's and tunnel's reassembly caps, which share
// channelwire.DefaultMaxIncompleteChunked, DO admit concurrent sequences and so
// genuinely need the cap -- this reasoning is about the Hub relay only.
func newChunkTracker(defaultReassembledMax int) *chunkTracker {
	return &chunkTracker{
		defaultReassembledMax: defaultReassembledMax,
		channelMax:            make(map[string]int),
		channels:              make(map[string]map[string]*channelChunkState),
	}
}

// SetChannelMax records the reassembled-message ceiling for channelID.
// Call with MaxReassembledMessageSize(effectivePayload) after negotiation.
// When the negotiated ceiling matches the tracker default, no per-channel
// entry is stored (maxFor already falls back to the default) — and any
// prior stricter override for that id is cleared.
func (ct *chunkTracker) SetChannelMax(channelID string, reassembledMax int) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if reassembledMax == ct.defaultReassembledMax {
		delete(ct.channelMax, channelID)
		return
	}
	ct.channelMax[channelID] = reassembledMax
}

// maxFor returns the reassembled ceiling for channelID. Caller holds ct.mu.
func (ct *chunkTracker) maxFor(channelID string) int {
	if max, ok := ct.channelMax[channelID]; ok {
		return max
	}
	return ct.defaultReassembledMax
}

// Track validates and tracks a chunk for the given channel+direction.
// Returns an error if the chunk should be rejected.
func (ct *chunkTracker) Track(channelID string, direction string, correlationID uint64, ciphertextLen int, more bool) error {
	// Reject correlation id 0: it is the reserved "no sequence in progress"
	// sentinel for channelChunkState.inProgressID. Both id allocators skip 0
	// (the tunnel client's allocateReqIDLocked and the frontend's nextRequestId
	// both start above it), so no well-behaved peer ever sends it -- but the
	// Hub relays a peer-supplied id it does not allocate and must not trust the
	// convention. Admitted, an id-0 chunk sets inProgressID back to the sentinel
	// (state.inProgressID = correlationID, below), which reads as "nothing in
	// progress" and lets a second concurrent sequence slip past the interleaving
	// guard -- the single-in-flight invariant this tracker exists to enforce.
	if correlationID == 0 {
		return fmt.Errorf("invalid chunk correlation id 0")
	}

	// Reject individual chunks that exceed the Noise transport limit.
	if ciphertextLen > maxChunkCiphertext {
		return fmt.Errorf("chunk ciphertext too large: %d > %d", ciphertextLen, maxChunkCiphertext)
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	if !more {
		// Final chunk or non-chunked message — clean up if tracked.
		dirs := ct.channels[channelID]
		if dirs == nil {
			return nil
		}
		state := dirs[direction]
		if state == nil {
			return nil
		}
		seq := state.sequences[correlationID]
		if seq != nil {
			// Validate final cumulative size.
			if err := ct.accumulate(channelID, direction, state, seq, correlationID, ciphertextLen); err != nil {
				return err
			}
			delete(state.sequences, correlationID)
		}
		if state.inProgressID == correlationID {
			state.inProgressID = 0
		}
		ct.cleanupEmpty(channelID, direction)
		return nil
	}

	// flags=MORE — in-progress chunk.
	dirs := ct.channels[channelID]
	if dirs == nil {
		dirs = make(map[string]*channelChunkState)
		ct.channels[channelID] = dirs
	}
	state := dirs[direction]
	if state == nil {
		state = &channelChunkState{
			sequences: make(map[uint64]*chunkSequence),
		}
		dirs[direction] = state
	}

	seq, exists := state.sequences[correlationID]
	if !exists {
		// New chunked sequence — admitted only when nothing else is in progress
		// for this channel+direction. That single rule is also what bounds
		// in-flight state: sequences is emptied and inProgressID cleared together
		// on every exit (completion, over-size rejection, channel removal), so the
		// map never holds more than the one admitted sequence.
		if state.inProgressID != 0 && state.inProgressID != correlationID {
			return fmt.Errorf("chunk interleaving: correlationID %d while %d is in-progress", correlationID, state.inProgressID)
		}
		seq = &chunkSequence{}
		state.sequences[correlationID] = seq
		state.inProgressID = correlationID
	}

	return ct.accumulate(channelID, direction, state, seq, correlationID, ciphertextLen)
}

// accumulate adds a chunk's estimated plaintext size to its sequence and enforces
// the cumulative cap, tearing the sequence down -- the map entry, the inProgressID
// sentinel when it is the one in progress, and the now-empty channel/direction
// state -- on a breach. One body for both the MORE and the final-chunk paths of
// Track, so the size math and the paired teardown cannot drift between them.
// Because the breach paths return before Track's own trailing cleanupEmpty,
// dropping the empty state here is what keeps them symmetric with the success
// path rather than stranding an empty entry until RemoveChannel. Caller holds
// ct.mu.
func (ct *chunkTracker) accumulate(channelID, direction string, state *channelChunkState, seq *chunkSequence, correlationID uint64, ciphertextLen int) error {
	estimated := ciphertextLen - channelwire.NoiseAEADAuthTagSize
	if estimated < 0 {
		estimated = 0
	}
	seq.estimatedPlaintext += estimated
	maxSize := ct.maxFor(channelID)
	if seq.estimatedPlaintext > maxSize {
		delete(state.sequences, correlationID)
		if state.inProgressID == correlationID {
			state.inProgressID = 0
		}
		ct.cleanupEmpty(channelID, direction)
		return fmt.Errorf("chunked message exceeds max size: %d > %d", seq.estimatedPlaintext, maxSize)
	}
	return nil
}

// RemoveChannel removes all tracking state for a channel, including any
// per-channel reassembled ceiling.
func (ct *chunkTracker) RemoveChannel(channelID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.channels, channelID)
	delete(ct.channelMax, channelID)
}

// cleanupEmpty removes empty state entries. Must be called with ct.mu held.
func (ct *chunkTracker) cleanupEmpty(channelID, direction string) {
	dirs := ct.channels[channelID]
	if dirs == nil {
		return
	}
	state := dirs[direction]
	if state != nil && len(state.sequences) == 0 {
		delete(dirs, direction)
	}
	if len(dirs) == 0 {
		delete(ct.channels, channelID)
	}
}
