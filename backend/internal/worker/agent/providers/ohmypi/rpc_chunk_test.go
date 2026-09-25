package ohmypi

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// splitFrame splits a frame into rpc_chunk frames of `size` bytes each, as omp's
// protocol version 2 does.
func splitFrame(id string, frame []byte, size int) []rpcChunkFrame {
	var chunks []rpcChunkFrame
	for start := 0; start < len(frame); start += size {
		end := min(start+size, len(frame))
		chunks = append(chunks, rpcChunkFrame{
			ChunkID:    id,
			Index:      len(chunks),
			ByteLength: len(frame),
			Data:       base64.StdEncoding.EncodeToString(frame[start:end]),
		})
	}
	for i := range chunks {
		chunks[i].Count = len(chunks)
	}
	return chunks
}

func TestChunkAssembler(t *testing.T) {
	t.Parallel()

	frame := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("é", 40) + `"}]}}`)

	t.Run("joins the chunks of one frame", func(t *testing.T) {
		var c chunkAssembler
		chunks := splitFrame("rpc-1", frame, 17)
		require.Greater(t, len(chunks), 2)
		for _, chunk := range chunks[:len(chunks)-1] {
			got, err := c.add(chunk)
			require.NoError(t, err)
			assert.Nil(t, got, "an incomplete frame yields nothing")
		}
		got, err := c.add(chunks[len(chunks)-1])
		require.NoError(t, err)
		assert.Equal(t, frame, got)
		assert.False(t, c.inProgress(), "the assembler holds nothing after a complete frame")
	})

	t.Run("a chunk that breaks the order drops the frame", func(t *testing.T) {
		var c chunkAssembler
		chunks := splitFrame("rpc-2", frame, 17)
		_, err := c.add(chunks[0])
		require.NoError(t, err)
		_, err = c.add(chunks[2])
		assert.ErrorIs(t, err, errChunkSequence)
		assert.False(t, c.inProgress())
		// The rest of the broken frame finds no frame to continue.
		_, err = c.add(chunks[3])
		assert.ErrorIs(t, err, errChunkSequence)
	})

	t.Run("a chunk of another frame drops the frame", func(t *testing.T) {
		var c chunkAssembler
		first := splitFrame("rpc-3", frame, 17)
		other := splitFrame("rpc-4", frame, 17)
		_, err := c.add(first[0])
		require.NoError(t, err)
		_, err = c.add(other[1])
		assert.ErrorIs(t, err, errChunkSequence)
	})

	t.Run("a new frame that starts early reports the lost one and keeps the new one", func(t *testing.T) {
		var c chunkAssembler
		lost := splitFrame("rpc-5", frame, 17)
		next := splitFrame("rpc-6", frame, 17)
		_, err := c.add(lost[0])
		require.NoError(t, err)
		_, err = c.add(next[0])
		assert.ErrorIs(t, err, errChunkSequence, "the frame that lost its tail is reported")
		var got []byte
		for _, chunk := range next[1:] {
			got, err = c.add(chunk)
			require.NoError(t, err)
		}
		assert.Equal(t, frame, got, "the frame that started over is kept whole")
	})

	t.Run("refuses a header omp's own decoder refuses", func(t *testing.T) {
		for name, chunk := range map[string]rpcChunkFrame{
			"empty id":       {ChunkID: "", Index: 0, Count: 2, ByteLength: 10, Data: ""},
			"long id":        {ChunkID: strings.Repeat("x", maxChunkIDLength+1), Index: 0, Count: 2, ByteLength: 10},
			"one chunk":      {ChunkID: "a", Index: 0, Count: 1, ByteLength: 10},
			"zero bytes":     {ChunkID: "a", Index: 0, Count: 2, ByteLength: 0},
			"negative bytes": {ChunkID: "a", Index: 0, Count: 2, ByteLength: -1},
		} {
			var c chunkAssembler
			_, err := c.add(chunk)
			assert.Errorf(t, err, "%s must be refused", name)
			assert.False(t, c.inProgress(), name)
		}
	})

	t.Run("refuses a frame over the limit", func(t *testing.T) {
		c := chunkAssembler{limit: 16}
		_, err := c.add(rpcChunkFrame{ChunkID: "a", Index: 0, Count: 2, ByteLength: 17})
		assert.ErrorContains(t, err, "the limit is 16")
	})

	t.Run("refuses a frame over the default limit when omp states none", func(t *testing.T) {
		var c chunkAssembler
		_, err := c.add(rpcChunkFrame{ChunkID: "a", Index: 0, Count: 2, ByteLength: defaultMaxReassembledFrameBytes + 1})
		assert.ErrorContains(t, err, "the limit is 67108864")
		assert.False(t, c.inProgress())
	})

	t.Run("accepts an id of the longest length omp's decoder accepts", func(t *testing.T) {
		var c chunkAssembler
		chunks := splitFrame(strings.Repeat("x", maxChunkIDLength), frame, 17)
		var got []byte
		for _, chunk := range chunks {
			var err error
			got, err = c.add(chunk)
			require.NoError(t, err)
		}
		assert.Equal(t, frame, got)
	})

	t.Run("a continuation that states another count or length drops the frame", func(t *testing.T) {
		for name, change := range map[string]func(*rpcChunkFrame){
			"another count":       func(chunk *rpcChunkFrame) { chunk.Count++ },
			"another byte length": func(chunk *rpcChunkFrame) { chunk.ByteLength++ },
		} {
			var c chunkAssembler
			chunks := splitFrame("rpc-7", frame, 17)
			_, err := c.add(chunks[0])
			require.NoError(t, err)
			next := chunks[1]
			change(&next)
			_, err = c.add(next)
			assert.ErrorIs(t, err, errChunkSequence, name)
			assert.False(t, c.inProgress(), name)
		}
	})

	t.Run("an early start with a bad header reports both frames", func(t *testing.T) {
		var c chunkAssembler
		_, err := c.add(splitFrame("rpc-8", frame, 17)[0])
		require.NoError(t, err)
		_, err = c.add(rpcChunkFrame{ChunkID: "rpc-9", Index: 0, Count: 1, ByteLength: 10})
		assert.ErrorIs(t, err, errChunkSequence, "the frame that lost its tail is reported")
		assert.ErrorContains(t, err, "a split frame has at least 2", "the refused header is reported too")
		assert.False(t, c.inProgress())
	})

	t.Run("refuses loose base64", func(t *testing.T) {
		var c chunkAssembler
		_, err := c.add(rpcChunkFrame{ChunkID: "a", Index: 0, Count: 2, ByteLength: 8, Data: "not base64!"})
		assert.ErrorContains(t, err, "invalid base64")
		assert.False(t, c.inProgress())
	})

	t.Run("refuses more bytes than the frame states", func(t *testing.T) {
		var c chunkAssembler
		_, err := c.add(rpcChunkFrame{ChunkID: "a", Index: 0, Count: 2, ByteLength: 2, Data: base64.StdEncoding.EncodeToString([]byte("abc"))})
		assert.ErrorContains(t, err, "more than the 2 bytes")
	})

	t.Run("refuses fewer bytes than the frame states", func(t *testing.T) {
		var c chunkAssembler
		_, err := c.add(rpcChunkFrame{ChunkID: "a", Index: 0, Count: 2, ByteLength: 10, Data: base64.StdEncoding.EncodeToString([]byte("ab"))})
		require.NoError(t, err)
		_, err = c.add(rpcChunkFrame{ChunkID: "a", Index: 1, Count: 2, ByteLength: 10, Data: base64.StdEncoding.EncodeToString([]byte("cd"))})
		assert.ErrorContains(t, err, "holds 4 bytes; it states 10")
	})

	t.Run("refuses a frame that is not UTF-8", func(t *testing.T) {
		var c chunkAssembler
		bad := []byte{0xff, 0xfe, 0xfd, 0xfc}
		var err error
		for _, chunk := range splitFrame("a", bad, 2) {
			_, err = c.add(chunk)
		}
		assert.ErrorContains(t, err, "not valid UTF-8")
	})
}

// chunkLine is the rpc_chunk frame that carries one chunk.
func chunkLine(t *testing.T, chunk rpcChunkFrame) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type": "rpc_chunk", "chunkId": chunk.ChunkID, "index": chunk.Index,
		"count": chunk.Count, "byteLength": chunk.ByteLength, "data": chunk.Data,
	})
}

// TestSplitFrameReachesItsReader pins that a reassembled frame takes the route a
// whole frame takes: a split RESPONSE reaches the caller that waits for it, and
// a split EVENT reaches the dispatcher.
func TestSplitFrameReachesItsReader(t *testing.T) {
	t.Parallel()

	t.Run("a split response reaches its caller", func(t *testing.T) {
		r := newRig(t)
		big := strings.Repeat("x", 3000)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type != CommandGetState {
				return nil
			}
			response := []byte(`{"type":"response","id":"` + command.ID + `","command":"get_state","success":true,"data":{"systemPrompt":["` + big + `"],"sessionId":"s2"}}`)
			chunks := splitFrame("rpc-9", response, 1000)
			go func() {
				// One goroutine, so the chunks arrive in order, as omp sends them.
				for _, chunk := range chunks {
					r.emit(chunkLine(t, chunk))
				}
			}()
			return &rigReply{Skip: true}
		})
		data, err := r.agent.sendCommand(CommandGetState, nil, 0)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"sessionId":"s2"`)
	})

	t.Run("a split event reaches the dispatcher", func(t *testing.T) {
		r := newRig(t)
		event := []byte(`{"type":"notice","level":"warning","message":"` + strings.Repeat("y", 2000) + `"}`)
		for _, chunk := range splitFrame("rpc-10", event, 700) {
			r.emit(chunkLine(t, chunk))
		}
		notifications := r.sink.PersistedNotifications()
		require.Len(t, notifications, 1)
		assert.Equal(t, event, notifications[0].Content)
	})

	// A chunk frame that cannot be decoded loses a slice of the frame in
	// progress. The rest of that frame is dropped, and the next frame arrives
	// whole.
	t.Run("an undecodable chunk drops the frame in progress", func(t *testing.T) {
		r := newRig(t)
		event := []byte(`{"type":"notice","level":"warning","message":"` + strings.Repeat("z", 2000) + `"}`)
		chunks := splitFrame("rpc-11", event, 700)
		r.emit(chunkLine(t, chunks[0]), `{"type":"rpc_chunk","chunkId":"rpc-11","index":"one"}`)
		for _, chunk := range chunks[1:] {
			r.emit(chunkLine(t, chunk))
		}
		assert.Zero(t, r.sink.NotificationCount(), "a frame with a missing slice is lost")
		assert.Empty(t, r.sink.Messages(), "no slice reaches the transcript as a frame of its own")

		for _, chunk := range splitFrame("rpc-12", event, 700) {
			r.emit(chunkLine(t, chunk))
		}
		notifications := r.sink.PersistedNotifications()
		require.Len(t, notifications, 1)
		assert.Equal(t, event, notifications[0].Content)
	})

	// The ready frame states omp's own reassembly limit, and the worker applies it.
	t.Run("the ready frame's limit applies", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"ready","protocolVersion":2,"supportedProtocolVersions":[1,2],"maxReassembledFrameBytes":1024}`)
		event := []byte(`{"type":"notice","level":"warning","message":"` + strings.Repeat("w", 2000) + `"}`)
		for _, chunk := range splitFrame("rpc-13", event, 700) {
			r.emit(chunkLine(t, chunk))
		}
		assert.Zero(t, r.sink.NotificationCount(), "a frame over omp's own limit is refused")
	})
}
