package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// TestEmitErrorLine_WritesValidJSON pins the on-wire shape so
// downstream `jq` filters can branch on `.source == "error"`.
func TestEmitErrorLine_WritesValidJSON(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{enc: json.NewEncoder(&buf)}

	require.NoError(t, em.emitError("agent-XYZ", "subscribe_failed", errors.New("boom")))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "error", got["source"])
	assert.Equal(t, "agent-XYZ", got["context"])
	assert.Equal(t, "subscribe_failed", got["code"])
	assert.Equal(t, "boom", got["message"])
}

// TestEmitErrorLine_ConcurrentEmissionStaysWellFormed exercises the
// mutex contract: two goroutines emitting concurrently must not
// interleave bytes mid-line. Without the mutex, a multi-byte JSON
// line from goroutine A could be shredded by goroutine B's encoder
// state and the consumer would see invalid JSON.
func TestEmitErrorLine_ConcurrentEmissionStaysWellFormed(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{enc: json.NewEncoder(&buf)}

	const n = 32
	wg := &sync.WaitGroup{}
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			errs <- em.emitError("ctx", "code", errors.New("err"))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	dec := json.NewDecoder(&buf)
	count := 0
	for dec.More() {
		var raw map[string]any
		require.NoError(t, dec.Decode(&raw), "concurrent emission produced malformed JSON")
		count++
	}
	assert.Equal(t, n, count, "every emit must produce exactly one line")
}

// TestEmitErrorLine_EmitsErrorMessageNotNil pins the nil-handling:
// callers may pass a nil error in theory, and the helper should
// crash loudly rather than emit `{"message": null}` that downstream
// consumers can't distinguish from a successful frame.
//
// We intentionally do NOT silently substitute a default — propagating
// nil is a programmer error and panicking surfaces it in tests.
func TestEmitErrorLine_PanicsOnNilError(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{enc: json.NewEncoder(&buf)}
	assert.Panics(t, func() {
		_ = em.emitError("ctx", "code", nil)
	})
}

type failingJSONLineWriter struct{ err error }

func (w failingJSONLineWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestLineEmitter_EmitReturnsAndLatchesWriteError(t *testing.T) {
	errWrite := errors.New("disk full")
	em := &lineEmitter{enc: json.NewEncoder(failingJSONLineWriter{err: errWrite})}

	require.ErrorIs(t, em.emit(map[string]any{"seq": 1}), errWrite)
	require.ErrorIs(t, em.emit(map[string]any{"seq": 2}), errWrite)
	require.ErrorIs(t, em.Err(), errWrite)
}

// TestRenderAgentMessage_DecompressesContentToJSON pins the core UX
// fix for `agent messages`: the zstd-compressed `content` blob must
// surface as parsed JSON, not a base64 string. Otherwise users
// piping the output through jq see an opaque payload they have to
// decode out-of-band.
func TestRenderAgentMessage_DecompressesContentToJSON(t *testing.T) {
	payload := []byte(`{"type":"text","text":"hello"}`)
	compressed, kind := msgcodec.Compress(payload)
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "m-1",
		Seq:                1,
		CreatedAt:          "2026-05-13T00:00:00Z",
		Content:            compressed,
		ContentCompression: kind,
	})
	content, ok := rendered["content"].(map[string]any)
	require.True(t, ok, "content must be parsed as a JSON object, got %T", rendered["content"])
	assert.Equal(t, "text", content["type"])
	assert.Equal(t, "hello", content["text"])
	_, hasCompression := rendered["content_compression"]
	assert.False(t, hasCompression, "content_compression must be stripped after decompression")
	_, hasRaw := rendered["content_raw"]
	assert.False(t, hasRaw, "happy-path render must not surface content_raw fallback")
}

// TestRenderAgentMessage_UncompressedContentParsedAsJSON covers the
// CONTENT_COMPRESSION_NONE branch: not every producer compresses, and
// the renderer must still parse the JSON instead of treating raw
// bytes as base64.
func TestRenderAgentMessage_UncompressedContentParsedAsJSON(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "m-1",
		Seq:                1,
		Content:            []byte(`["a","b"]`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	content, ok := rendered["content"].([]any)
	require.True(t, ok, "content must be parsed as a JSON array, got %T", rendered["content"])
	assert.Equal(t, []any{"a", "b"}, content)
}

// TestRenderAgentMessage_NonJSONContentFallsBackToString pins the
// graceful-degradation path: when the worker ships plain text (older
// providers, marker frames), the renderer keeps it visible as a
// string rather than dropping it on a Unmarshal error.
func TestRenderAgentMessage_NonJSONContentFallsBackToString(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "m-1",
		Seq:                1,
		Content:            []byte("hello plain text"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, "hello plain text", rendered["content"])
}

// TestRenderAgentMessage_ParsesSpanLines pins the second readability
// fix: span_lines arrives on the wire as a JSON-encoded string, so
// without parsing the caller would see a literal escaped string
// instead of the structured array the field describes.
func TestRenderAgentMessage_ParsesSpanLines(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:        "m-1",
		Seq:       1,
		SpanLines: `[{"span_id":"toolu_1","color":5,"type":"connector_end"}]`,
	})
	lines, ok := rendered["span_lines"].([]any)
	require.True(t, ok, "span_lines must be parsed as a JSON array, got %T", rendered["span_lines"])
	require.Len(t, lines, 1)
	first, ok := lines[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "toolu_1", first["span_id"])
	assert.EqualValues(t, 5, first["color"])
	assert.Equal(t, "connector_end", first["type"])
}

// TestRenderAgentMessage_EmptySpanLinesOmitted pins the proto3
// zero-value contract: an empty span_lines must not show up at all,
// so callers don't have to special-case `"span_lines": ""`.
func TestRenderAgentMessage_EmptySpanLinesOmitted(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 1})
	_, has := rendered["span_lines"]
	assert.False(t, has)
}

// TestRenderAgentMessage_PreviousSeqMarksAReseq pins the supersession marker: a live
// reseq broadcast (previous_seq > 0) surfaces `previous_seq` so a --follow consumer can
// reconcile the moved row by id instead of seeing an unexplained duplicate.
func TestRenderAgentMessage_PreviousSeqMarksAReseq(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 55, PreviousSeq: 20})
	assert.Equal(t, int64(20), rendered["previous_seq"])
}

// TestRenderAgentMessage_ZeroPreviousSeqOmitted pins the proto3 zero-value contract:
// a normal (non-reseq) row, a single-page read, or a replay carries previous_seq 0 and
// the field must not appear, so consumers don't special-case `"previous_seq": 0`.
func TestRenderAgentMessage_ZeroPreviousSeqOmitted(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 1})
	_, has := rendered["previous_seq"]
	assert.False(t, has)
}

// TestRenderAgentMessage_DecompressFailureSurfacesError pins the
// failure path: a corrupted zstd payload must be reported, and the
// raw bytes must remain accessible so the caller can salvage the
// blob with an external tool.
func TestRenderAgentMessage_DecompressFailureSurfacesError(t *testing.T) {
	rendered := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "m-1",
		Seq:                1,
		Content:            []byte("not zstd"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
	})
	_, ok := rendered["content_error"].(string)
	assert.True(t, ok, "content_error must be a string describing the failure")
	raw, ok := rendered["content_raw"].([]byte)
	require.True(t, ok)
	assert.Equal(t, []byte("not zstd"), raw)
}
