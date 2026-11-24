package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// TestLineEmitter_ConcurrentEmissionStaysWellFormed exercises the mutex
// contract: two goroutines emitting concurrently must not interleave bytes
// mid-line. Without the mutex, a multi-byte JSON line from goroutine A could
// be shredded by goroutine B's write and the consumer would see invalid
// JSON.
func TestLineEmitter_ConcurrentEmissionStaysWellFormed(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{printer: &cli.Printer{Out: &buf, Err: &bytes.Buffer{}, Format: cli.FormatJSON}}

	const n = 32
	wg := &sync.WaitGroup{}
	wg.Add(n)
	errs := make(chan error, n)
	for i := range n {
		go func() {
			defer wg.Done()
			errs <- em.emit(map[string]any{"seq": i, "text": strings.Repeat("x", 512)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, n, "every emit must produce exactly one line")
	for _, line := range lines {
		var env struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &env), "concurrent emission produced malformed JSON")
		require.NotNil(t, env.Data, "each line is a data envelope")
	}
}

type failingJSONLineWriter struct{ err error }

func (w failingJSONLineWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestLineEmitter_EmitReturnsAndLatchesWriteError(t *testing.T) {
	errWrite := errors.New("disk full")
	em := &lineEmitter{printer: &cli.Printer{Out: failingJSONLineWriter{err: errWrite}, Format: cli.FormatJSON}}

	require.ErrorIs(t, em.emit(map[string]any{"seq": 1}), errWrite)
	require.ErrorIs(t, em.emit(map[string]any{"seq": 2}), errWrite)
	require.ErrorIs(t, em.Err(), errWrite)
}

func TestLineEmitter_EmitFollowEventWritesRetractionThenMessage(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{printer: &cli.Printer{Out: &buf, Format: cli.FormatJSON}}

	emitted, err := em.emitFollowEvent(&leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_MessagesRetracted{
			MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{Seqs: []int64{19}},
		},
	})
	require.NoError(t, err)
	assert.True(t, emitted, "a deletion produces a follow record")
	emitted, err = em.emitFollowEvent(&leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_AgentMessage{
			AgentMessage: &leapmuxv1.AgentChatMessage{Id: "m-17", Seq: 17},
		},
	})
	require.NoError(t, err)
	assert.True(t, emitted)

	dec := json.NewDecoder(&buf)
	var retraction json.RawMessage
	require.NoError(t, dec.Decode(&retraction))
	assert.JSONEq(t, `{"data":{"type":"messages_retracted","seqs":[19]}}`, string(retraction))
	var message struct {
		Data struct {
			ID  string `json:"id"`
			Seq int64  `json:"seq"`
		} `json:"data"`
	}
	require.NoError(t, dec.Decode(&message))
	assert.Equal(t, "m-17", message.Data.ID)
	assert.Equal(t, int64(17), message.Data.Seq)
	assert.False(t, dec.More(), "each event produces one record")
}

func TestLineEmitter_EmitFollowEventIgnoresEmptyRetraction(t *testing.T) {
	var buf bytes.Buffer
	em := &lineEmitter{printer: &cli.Printer{Out: &buf, Format: cli.FormatJSON}}

	emitted, err := em.emitFollowEvent(&leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_MessagesRetracted{
			MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{},
		},
	})
	require.NoError(t, err)
	assert.False(t, emitted)
	assert.Empty(t, buf.String())
}

func TestLineEmitter_EmitFollowEventReturnsRetractionWriteError(t *testing.T) {
	errWrite := errors.New("disk full")
	em := &lineEmitter{printer: &cli.Printer{Out: failingJSONLineWriter{err: errWrite}, Format: cli.FormatJSON}}

	_, err := em.emitFollowEvent(&leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_MessagesRetracted{
			MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{Seqs: []int64{19}},
		},
	})
	require.ErrorIs(t, err, errWrite)
	require.ErrorIs(t, em.Err(), errWrite)
}

// renderedMessage renders m as `agent messages --json` prints it.
func renderedMessage(t *testing.T, m *leapmuxv1.AgentChatMessage) map[string]any {
	t.Helper()
	return jsonMap(t, renderAgentMessage(m))
}

// A message prints as one readable text line: its seq, its source, and its
// extracted human-readable text on one line (with --raw showing compact JSON).
func TestAgentMessage_TextLine(t *testing.T) {
	m := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Seq:                7,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:            []byte("{\"text\":\n\"hi\"}"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, `#7 agent: hi`, m.TextLine())
	m.rawText = true
	assert.Equal(t, `#7 agent: {"text":"hi"}`, m.TextLine())
	long := renderAgentMessage(&leapmuxv1.AgentChatMessage{Seq: 1, Content: []byte(strings.Repeat("a", maxSummaryRunes*2)), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE})
	assert.Equal(t, maxSummaryRunes, len([]rune(strings.TrimPrefix(long.TextLine(), "#1 : "))), "a long content is cut to one screen line")
}

// TestRenderAgentMessage_DecompressesContentToJSON pins the core UX
// fix for `agent messages`: the zstd-compressed `content` blob must
// surface as parsed JSON, not a base64 string. Otherwise users
// piping the output through jq see an opaque payload they have to
// decode out-of-band.
func TestRenderAgentMessage_DecompressesContentToJSON(t *testing.T) {
	payload := []byte(`{"type":"text","text":"hello"}`)
	compressed, kind := msgcodec.Compress(payload)
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{
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
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{
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
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{
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
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{
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
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 1})
	_, has := rendered["span_lines"]
	assert.False(t, has)
}

// TestRenderAgentMessage_PreviousSeqMarksAReseq pins the supersession marker: a live
// reseq broadcast (previous_seq > 0) surfaces `previous_seq` so a --follow consumer can
// reconcile the moved row by id instead of seeing an unexplained duplicate.
func TestRenderAgentMessage_PreviousSeqMarksAReseq(t *testing.T) {
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 55, PreviousSeq: 20})
	assert.EqualValues(t, 20, rendered["previous_seq"])
}

// TestRenderAgentMessage_ZeroPreviousSeqOmitted pins the proto3 zero-value contract:
// a normal (non-reseq) row, a single-page read, or a replay carries previous_seq 0 and
// the field must not appear, so consumers don't special-case `"previous_seq": 0`.
func TestRenderAgentMessage_ZeroPreviousSeqOmitted(t *testing.T) {
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{Id: "m-1", Seq: 1})
	_, has := rendered["previous_seq"]
	assert.False(t, has)
}

// TestRenderAgentMessage_DecompressFailureSurfacesError pins the
// failure path: a corrupted zstd payload must be reported, and the
// raw bytes must remain accessible so the caller can salvage the
// blob with an external tool.
func TestRenderAgentMessage_DecompressFailureSurfacesError(t *testing.T) {
	rendered := renderedMessage(t, &leapmuxv1.AgentChatMessage{
		Id:                 "m-1",
		Seq:                1,
		Content:            []byte("not zstd"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
	})
	_, ok := rendered["content_error"].(string)
	assert.True(t, ok, "content_error must be a string describing the failure")
	encoded, ok := rendered["content_raw"].(string)
	require.True(t, ok, "JSON carries the raw bytes as base64")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, []byte("not zstd"), raw)
}
