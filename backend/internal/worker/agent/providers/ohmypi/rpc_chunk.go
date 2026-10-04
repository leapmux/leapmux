package ohmypi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Limits of omp's RPC protocol version 2, from `modes/rpc/rpc-frame.ts`.
//
// omp states the reassembly limit in its ready frame, and the worker reads it from
// there (see readyFrame). The constant below is what the worker uses when the
// ready frame states none, and it is what omp 18.2.11 sends.
const (
	// defaultMaxReassembledFrameBytes is the largest frame omp splits into
	// chunks. omp replaces a larger one with an overflow frame.
	defaultMaxReassembledFrameBytes = 64 << 20
	// maxChunkIDLength is the longest chunk id omp's reference decoder accepts.
	maxChunkIDLength = 128
)

// rpcChunkFrame is one rpc_chunk frame: one base64 slice of the UTF-8 JSON of a
// frame too large to send whole.
type rpcChunkFrame struct {
	ChunkID    string `json:"chunkId"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	ByteLength int    `json:"byteLength"`
	Data       string `json:"data"`
}

// errChunkSequence reports a chunk that does not continue the frame the
// assembler holds. The caller logs it and drops the frame, because a frame with a
// missing slice cannot be recovered.
var errChunkSequence = errors.New("rpc_chunk out of sequence")

// chunkAssembler joins the rpc_chunk frames of one split frame.
//
// omp sends the chunks of one frame consecutively and never interleaves two
// frames, so the assembler holds at most one frame. It applies the checks of
// omp's own reference decoder: a chunk id of at most 128 characters, a count of
// at least two, strict base64, the exact byte length, and valid UTF-8.
//
// Only the read loop calls it, so it holds no lock.
type chunkAssembler struct {
	// limit is the largest frame the assembler accepts. Zero selects
	// defaultMaxReassembledFrameBytes.
	limit int

	chunkID    string
	count      int
	next       int
	byteLength int
	buf        []byte
}

// reset drops the frame in progress.
func (c *chunkAssembler) reset() {
	c.chunkID = ""
	c.count = 0
	c.next = 0
	c.byteLength = 0
	c.buf = nil
}

// inProgress reports whether the assembler holds part of a frame.
func (c *chunkAssembler) inProgress() bool {
	return c.chunkID != ""
}

func (c *chunkAssembler) maxBytes() int {
	if c.limit > 0 {
		return c.limit
	}
	return defaultMaxReassembledFrameBytes
}

// add takes one chunk. It returns the whole frame when the chunk completes it,
// nil while the frame is incomplete, and an error for a chunk that breaks the
// sequence or the frame's limits. An error drops the frame in progress, and a
// chunk with index 0 after an error starts the next frame cleanly.
func (c *chunkAssembler) add(chunk rpcChunkFrame) ([]byte, error) {
	if chunk.Index == 0 {
		// A new frame. A frame still in progress lost its tail, so it is dropped
		// and the caller hears about it.
		var abandoned error
		if c.inProgress() {
			abandoned = fmt.Errorf("%w: chunk %q started before chunk %q completed", errChunkSequence, chunk.ChunkID, c.chunkID)
		}
		c.reset()
		if err := c.start(chunk); err != nil {
			c.reset()
			return nil, errors.Join(abandoned, err)
		}
		if abandoned != nil {
			// The new frame starts; only the old one is lost. Report it without
			// dropping the new one.
			return c.append(chunk, abandoned)
		}
		return c.append(chunk, nil)
	}
	if !c.inProgress() || chunk.ChunkID != c.chunkID || chunk.Index != c.next || chunk.Count != c.count || chunk.ByteLength != c.byteLength {
		err := fmt.Errorf("%w: chunk %q index %d does not continue chunk %q at index %d", errChunkSequence, chunk.ChunkID, chunk.Index, c.chunkID, c.next)
		c.reset()
		return nil, err
	}
	return c.append(chunk, nil)
}

// start validates the header of a frame's first chunk and records it.
func (c *chunkAssembler) start(chunk rpcChunkFrame) error {
	switch {
	case chunk.ChunkID == "" || len(chunk.ChunkID) > maxChunkIDLength:
		return fmt.Errorf("rpc_chunk id must hold 1 to %d characters", maxChunkIDLength)
	case chunk.Count < 2:
		return fmt.Errorf("rpc_chunk %q states %d chunks; a split frame has at least 2", chunk.ChunkID, chunk.Count)
	case chunk.ByteLength <= 0 || chunk.ByteLength > c.maxBytes():
		return fmt.Errorf("rpc_chunk %q states %d bytes; the limit is %d", chunk.ChunkID, chunk.ByteLength, c.maxBytes())
	}
	c.chunkID = chunk.ChunkID
	c.count = chunk.Count
	c.byteLength = chunk.ByteLength
	c.buf = make([]byte, 0, chunk.ByteLength)
	return nil
}

// append decodes one chunk into the frame and returns the frame when the chunk
// is its last. prior is an error to report beside the result.
func (c *chunkAssembler) append(chunk rpcChunkFrame, prior error) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(chunk.Data)
	if err != nil {
		c.reset()
		return nil, errors.Join(prior, fmt.Errorf("rpc_chunk %q index %d: invalid base64: %w", chunk.ChunkID, chunk.Index, err))
	}
	if len(c.buf)+len(decoded) > c.byteLength {
		id := c.chunkID
		c.reset()
		return nil, errors.Join(prior, fmt.Errorf("rpc_chunk %q holds more than the %d bytes it states", id, chunk.ByteLength))
	}
	c.buf = append(c.buf, decoded...)
	c.next++
	if c.next < c.count {
		return nil, prior
	}
	frame := c.buf
	id := c.chunkID
	c.reset()
	if len(frame) != chunk.ByteLength {
		return nil, errors.Join(prior, fmt.Errorf("rpc_chunk %q holds %d bytes; it states %d", id, len(frame), chunk.ByteLength))
	}
	if !utf8.Valid(frame) {
		return nil, errors.Join(prior, fmt.Errorf("rpc_chunk %q is not valid UTF-8", id))
	}
	return frame, prior
}
