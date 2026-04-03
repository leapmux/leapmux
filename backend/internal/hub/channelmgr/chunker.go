package channelmgr

import (
	"fmt"
	"sync"
)

const (
	// noiseAuthTagSize is the Noise AEAD auth tag overhead per ciphertext.
	noiseAuthTagSize = 16

	// maxChunkCiphertext is the maximum ciphertext size for a single Noise
	// transport message (65535 bytes = 65519 plaintext + 16 auth tag).
	maxChunkCiphertext = 65535
)

// chunkSequence tracks cumulative size for one in-flight chunked message.
type chunkSequence struct {
	estimatedPlaintext int
}

// channelChunkState tracks all in-flight chunk sequences for one channel+direction.
type channelChunkState struct {
	sequences    map[uint32]*chunkSequence // correlationID -> sequence
	inProgressID uint32                    // correlationID of current in-progress chunked send (0 = none)
}

// chunkTracker enforces message size limits and interleaving rules for
// chunked channel messages relayed through the Hub. The Hub cannot decrypt,
// but estimates plaintext size from ciphertext (ciphertext - 16 bytes auth tag).
type chunkTracker struct {
	mu                   sync.Mutex
	maxMessageSize       int
	maxIncompleteChunked int
	// channels maps channelID -> direction ("fe2w" or "w2fe") -> state
	channels map[string]map[string]*channelChunkState
}

func newChunkTracker(maxMessageSize, maxIncompleteChunked int) *chunkTracker {
	return &chunkTracker{
		maxMessageSize:       maxMessageSize,
		maxIncompleteChunked: maxIncompleteChunked,
		channels:             make(map[string]map[string]*channelChunkState),
	}
}

// Track validates and tracks a chunk for the given channel+direction.
// Returns an error if the chunk should be rejected.
func (ct *chunkTracker) Track(channelID string, direction string, correlationID uint32, ciphertextLen int, more bool) error {
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
			estimated := ciphertextLen - noiseAuthTagSize
			if estimated < 0 {
				estimated = 0
			}
			seq.estimatedPlaintext += estimated
			if seq.estimatedPlaintext > ct.maxMessageSize {
				delete(state.sequences, correlationID)
				if state.inProgressID == correlationID {
					state.inProgressID = 0
				}
				return fmt.Errorf("chunked message exceeds max size: %d > %d", seq.estimatedPlaintext, ct.maxMessageSize)
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
			sequences: make(map[uint32]*chunkSequence),
		}
		dirs[direction] = state
	}

	seq, exists := state.sequences[correlationID]
	if !exists {
		// New chunked sequence — validate interleaving and max incomplete.
		if state.inProgressID != 0 && state.inProgressID != correlationID {
			return fmt.Errorf("chunk interleaving: correlationID %d while %d is in-progress", correlationID, state.inProgressID)
		}
		if len(state.sequences) >= ct.maxIncompleteChunked {
			return fmt.Errorf("too many incomplete chunked messages: %d", len(state.sequences))
		}
		seq = &chunkSequence{}
		state.sequences[correlationID] = seq
		state.inProgressID = correlationID
	}

	estimated := ciphertextLen - noiseAuthTagSize
	if estimated < 0 {
		estimated = 0
	}
	seq.estimatedPlaintext += estimated
	if seq.estimatedPlaintext > ct.maxMessageSize {
		delete(state.sequences, correlationID)
		if state.inProgressID == correlationID {
			state.inProgressID = 0
		}
		return fmt.Errorf("chunked message exceeds max size: %d > %d", seq.estimatedPlaintext, ct.maxMessageSize)
	}

	return nil
}

// RemoveChannel removes all tracking state for a channel.
func (ct *chunkTracker) RemoveChannel(channelID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.channels, channelID)
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
