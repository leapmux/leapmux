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

// TestSplitKV_HappyPath covers the simplest case: one '=' produces
// (key, value, nil).
func TestSplitKV_HappyPath(t *testing.T) {
	k, v, err := splitKV("foo=bar")
	require.NoError(t, err)
	assert.Equal(t, "foo", k)
	assert.Equal(t, "bar", v)
}

// TestSplitKV_FirstEqualsWins pins the "value contains '='" case
// (e.g. base64-encoded values, query strings). Splitting on every
// '=' would corrupt values like "csrf=abc=def==".
func TestSplitKV_FirstEqualsWins(t *testing.T) {
	k, v, err := splitKV("csrf=abc=def==")
	require.NoError(t, err)
	assert.Equal(t, "csrf", k)
	assert.Equal(t, "abc=def==", v)
}

// TestSplitKV_EmptyValueAllowed lets `--extra-setting key=` clear an
// existing setting on the agent. Without this, users would have no
// way to send "set this to empty".
func TestSplitKV_EmptyValueAllowed(t *testing.T) {
	k, v, err := splitKV("key=")
	require.NoError(t, err)
	assert.Equal(t, "key", k)
	assert.Equal(t, "", v)
}

// TestSplitKV_EmptyKeyAllowed documents the symmetric edge: a leading
// '=' produces an empty key. The CLI passes this through to the
// worker, which is the right layer to validate semantically.
func TestSplitKV_EmptyKeyAllowed(t *testing.T) {
	k, v, err := splitKV("=value")
	require.NoError(t, err)
	assert.Equal(t, "", k)
	assert.Equal(t, "value", v)
}

// TestSplitKV_NoEqualsRejected covers the user error case:
// `--extra-setting key` without a value should produce a clear hint
// rather than silently mapping to "key" → "" or vice versa.
func TestSplitKV_NoEqualsRejected(t *testing.T) {
	_, _, err := splitKV("flag-only")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key=value")
}

// TestSplitKV_EmptyStringRejected pins the trivial bad case so we
// don't accept an entirely empty `--extra-setting` value silently.
func TestSplitKV_EmptyStringRejected(t *testing.T) {
	_, _, err := splitKV("")
	require.Error(t, err)
}

// TestStringSliceFlag_AccumulatesValues is the multi-pass `flag.Value`
// contract: each `Set` appends so `--extra-setting a=1 --extra-setting b=2`
// produces ["a=1", "b=2"].
func TestStringSliceFlag_AccumulatesValues(t *testing.T) {
	s := stringSliceFlag{}
	require.NoError(t, s.Set("a=1"))
	require.NoError(t, s.Set("b=2"))
	require.NoError(t, s.Set("c=3"))
	assert.Equal(t, []string{"a=1", "b=2", "c=3"}, s.values)
}

// TestStringSliceFlag_StringIsHumanReadable covers the small
// `String()` implementation. flag.PrintDefaults uses this for help
// text; an unreadable representation would confuse users running
// `leapmux remote agent set --help`.
func TestStringSliceFlag_StringIsHumanReadable(t *testing.T) {
	s := stringSliceFlag{values: []string{"a=1", "b=2"}}
	got := s.String()
	assert.Contains(t, got, "a=1")
	assert.Contains(t, got, "b=2")
}

// TestStringSliceFlag_EmptyStartsAsNilNotEmptySlice pins the
// initial state so callers can branch on `len(extras.values) == 0`
// without worrying about a non-nil-but-empty distinction.
func TestStringSliceFlag_EmptyStartsAsNilNotEmptySlice(t *testing.T) {
	s := stringSliceFlag{}
	assert.Empty(t, s.values)
	assert.Nil(t, s.values)
}

// TestEmitErrorLine_WritesValidJSON pins the on-wire shape so
// downstream `jq` filters can branch on `.source == "error"`.
func TestEmitErrorLine_WritesValidJSON(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	mu := &sync.Mutex{}

	emitErrorLine(enc, mu, "agent-XYZ", "subscribe_failed", errors.New("boom"))

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
	enc := json.NewEncoder(&buf)
	mu := &sync.Mutex{}

	const n = 32
	wg := &sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			emitErrorLine(enc, mu, "ctx", "code", errors.New("err"))
		}()
	}
	wg.Wait()

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
	enc := json.NewEncoder(&buf)
	mu := &sync.Mutex{}
	assert.Panics(t, func() {
		emitErrorLine(enc, mu, "ctx", "code", nil)
	})
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
