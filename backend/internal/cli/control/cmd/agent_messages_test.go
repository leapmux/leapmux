package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/channel"
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
// Worker-provided ReplyText (with --raw showing compact JSON). Without
// ReplyText, plain string content prints directly and structured JSON falls
// back to compact JSON with no provider-key heuristics.
func TestAgentMessage_TextLine(t *testing.T) {
	reply := "hi"
	m := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Seq:                7,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		ReplyText:          &reply,
		Content:            []byte("{\"text\":\n\"hi\"}"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, `#7 agent: hi`, m.TextLine())
	assert.Equal(t, `#7 agent: {"text":"hi"}`, m.formatText(messageFormatOptions{raw: true}))

	noReply := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Seq:                8,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:            []byte("{\"text\":\n\"hi\"}"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, `#8 agent: {"text":"hi"}`, noReply.TextLine())
	assert.Equal(t, "### #8 agent\n\n{\"text\":\"hi\"}", noReply.formatText(messageFormatOptions{markdown: true}))

	plain := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Seq:                9,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            []byte("line 1\nline 2"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, "#9 user: line 1 line 2", plain.TextLine())
	assert.Equal(t, "### #9 user\n\nline 1\nline 2", plain.formatText(messageFormatOptions{markdown: true}))

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

func TestAgentMessage_RenderTextAndQuietLines(t *testing.T) {
	longText := strings.Repeat("b", maxSummaryRunes+25)
	msg := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "m-42",
		Seq:                42,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		ReplyText:          &longText,
		Content:            []byte(`{"text":"` + longText + `"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	fullMsg := formattedAgentMessage{
		agentMessage: msg,
		opts:         messageFormatOptions{full: true},
	}

	var textBuf bytes.Buffer
	p := &cli.Printer{Out: &textBuf, Err: &bytes.Buffer{}, Format: cli.FormatText}
	require.NoError(t, p.Result(fullMsg))
	assert.Equal(t, "#42 agent: "+longText+"\n", textBuf.String(), "RenderText honors full without truncating")

	textBuf.Reset()
	require.NoError(t, p.Event(fullMsg))
	assert.Equal(t, "#42 agent: "+longText+"\n", textBuf.String(), "Event honors full without truncating")

	rawMsg := formattedAgentMessage{
		agentMessage: msg,
		opts:         messageFormatOptions{raw: true, full: true},
	}
	textBuf.Reset()
	require.NoError(t, p.Result(rawMsg))
	assert.Equal(t, "#42 agent: {\"text\":\""+longText+"\"}\n", textBuf.String(), "RenderText honors --raw")

	mdMsg := formattedAgentMessage{
		agentMessage: msg,
		opts:         messageFormatOptions{markdown: true},
	}
	textBuf.Reset()
	require.NoError(t, p.Result(mdMsg))
	assert.Equal(t, "### #42 agent\n\n"+longText+"\n", textBuf.String(), "RenderText honors --markdown")

	// Non-text formats (JSON, NDJSON, TSV, quiet) must render identically for
	// formattedAgentMessage and bare agentMessage, with no leaked opts field.
	for _, tc := range []struct {
		name   string
		format cli.Format
	}{
		{name: "json", format: cli.FormatJSON},
		{name: "ndjson", format: cli.FormatNDJSON},
		{name: "tsv", format: cli.FormatTSV},
		{name: "quiet", format: cli.FormatQuiet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wantRes, gotRes bytes.Buffer
			require.NoError(t, (&cli.Printer{Out: &wantRes, Err: &bytes.Buffer{}, Format: tc.format}).Result(msg))
			require.NoError(t, (&cli.Printer{Out: &gotRes, Err: &bytes.Buffer{}, Format: tc.format}).Result(rawMsg))
			assert.Equal(t, wantRes.String(), gotRes.String(), "Result format %s must match bare agentMessage", tc.name)
			assert.NotContains(t, gotRes.String(), "opts")

			var wantEvt, gotEvt bytes.Buffer
			require.NoError(t, (&cli.Printer{Out: &wantEvt, Err: &bytes.Buffer{}, Format: tc.format}).Event(msg))
			require.NoError(t, (&cli.Printer{Out: &gotEvt, Err: &bytes.Buffer{}, Format: tc.format}).Event(rawMsg))
			assert.Equal(t, wantEvt.String(), gotEvt.String(), "Event format %s must match bare agentMessage", tc.name)
			assert.NotContains(t, gotEvt.String(), "opts")

			if tc.format == cli.FormatQuiet {
				assert.Equal(t, "42\n", gotRes.String(), "QuietLines prints integer sequence number")
				assert.Equal(t, "42\n", gotEvt.String(), "Event in quiet mode prints integer sequence number matching paged -q")
			}
		})
	}
}

func TestAgentMessage_TextLine_ContentErrorAndNilContent(t *testing.T) {
	t.Run("content decode error in text and markdown modes", func(t *testing.T) {
		msg := renderAgentMessage(&leapmuxv1.AgentChatMessage{
			Id:                 "m-err",
			Seq:                9,
			Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			Content:            []byte("corrupt"),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
		})
		require.NotEmpty(t, msg.ContentError)
		assert.Contains(t, msg.TextLine(), "#9 agent: [content decode error: ")
		assert.Contains(t, msg.formatText(messageFormatOptions{markdown: true}), "### #9 agent\n\n[content decode error: ")
	})

	t.Run("nil content renders empty body instead of literal null", func(t *testing.T) {
		msg := agentMessage{Seq: 3, Source: "agent"}
		assert.Equal(t, "#3 agent: ", msg.TextLine())
		assert.Equal(t, "#3 agent: ", msg.formatText(messageFormatOptions{raw: true}))
		assert.Equal(t, "### #3 agent\n\n", msg.formatText(messageFormatOptions{markdown: true}))
		assert.Equal(t, "### #3 agent\n\n```json\n\n```", msg.formatText(messageFormatOptions{raw: true, markdown: true}))
	})
}

func TestContentSummaryOpts_PreservesSpacesInJSONAndTruncatesRunes(t *testing.T) {
	spaced := map[string]any{"text": "a   b    c"}
	assert.Equal(t, `{"text":"a   b    c"}`, contentSummaryOpts(spaced, true), "must preserve consecutive spaces inside JSON string literals")
	assert.Equal(t, `{"text":"a   b    c"}`, contentSummaryOpts(spaced, false))

	longJSON := map[string]any{"text": "keep   spaces " + strings.Repeat("가", maxSummaryRunes)}
	truncated := contentSummaryOpts(longJSON, false)
	assert.True(t, utf8.ValidString(truncated), "JSON rune truncation must not split multi-byte UTF-8 sequences")
	assert.Contains(t, truncated, "keep   spaces ", "truncation must not collapse spaces in JSON strings")
	assert.Equal(t, maxSummaryRunes, len([]rune(truncated)), "truncation must count runes, not bytes")
	assert.True(t, strings.HasSuffix(truncated, "…"))

	longPlain := strings.Repeat("한", maxSummaryRunes+10)
	truncatedPlain := contentSummaryOpts(longPlain, false)
	assert.True(t, utf8.ValidString(truncatedPlain), "plain-text rune truncation must not split multi-byte UTF-8 sequences")
	assert.Equal(t, maxSummaryRunes, len([]rune(truncatedPlain)))
	assert.True(t, strings.HasSuffix(truncatedPlain, "…"))
	assert.Equal(t, longPlain, contentSummaryOpts(longPlain, true), "full mode preserves every multi-byte rune")

	assert.Equal(t, "", contentSummaryOpts(make(chan int), false), "unmarshalable content falls back to empty string")
}

func TestAgentMessages_FailsFastOnLocalValidationErrors(t *testing.T) {
	clearRemoteEnv(t)
	id := fullTestID("ag-1")
	for name, tc := range map[string]struct {
		args    []string
		wantMsg string
	}{
		"follow with summary":        {[]string{"agent", "messages", id, "--follow", "--summary", "--hub", "https://stub.invalid"}, "--follow cannot be combined with --summary"},
		"explicit empty anchor":      {[]string{"agent", "messages", id, "--anchor", "", "--hub", "https://stub.invalid"}, "invalid --anchor"},
		"explicit whitespace anchor": {[]string{"agent", "messages", id, "--anchor", "   ", "--hub", "https://stub.invalid"}, "invalid --anchor"},
		"before without cursor":      {[]string{"agent", "messages", id, "--anchor", "before", "--hub", "https://stub.invalid"}, "--anchor before/after requires --cursor-seq"},
		"follow with oldest":         {[]string{"agent", "messages", id, "--follow", "--anchor", "oldest", "--hub", "https://stub.invalid"}, "--follow cannot be combined with --anchor oldest or before"},
		"non-positive limit":         {[]string{"agent", "messages", id, "--limit", "0", "--hub", "https://stub.invalid"}, "--limit must be greater than 0"},
		"omitted agent outside tab":  {[]string{"agent", "messages", "--hub", "https://stub.invalid"}, "does not run inside a LeapMux agent tab"},
		"self outside tab":           {[]string{"agent", "messages", "self", "--hub", "https://stub.invalid"}, "does not run inside a LeapMux agent tab"},
	} {
		t.Run(name, func(t *testing.T) {
			f := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.wantMsg)
		})
	}
}

type summaryMessagesWorker struct {
	mu            sync.Mutex
	messages      []*leapmuxv1.AgentChatMessage
	goal          *leapmuxv1.AgentGoalSnapshot
	todos         *leapmuxv1.AgentTodoSnapshot
	tasks         *leapmuxv1.AgentBackgroundTaskSnapshot
	lastListReq   *leapmuxv1.ListAgentMessagesRequest
	lastGetMsgReq *leapmuxv1.GetAgentMessageRequest
}

func (w *summaryMessagesWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		var in leapmuxv1.ListAgentMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.lastListReq = &in
		w.mu.Unlock()
		var latest int64
		if len(w.messages) > 0 {
			latest = w.messages[len(w.messages)-1].GetSeq()
		}
		writeProtoResponse(rw, &leapmuxv1.ListAgentMessagesResponse{
			Messages:        w.messages,
			LatestSeq:       &latest,
			Goal:            w.goal,
			Todos:           w.todos,
			BackgroundTasks: w.tasks,
		})
	case leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure:
		var in leapmuxv1.GetAgentMessageRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.lastGetMsgReq = &in
		w.mu.Unlock()
		var msg *leapmuxv1.AgentChatMessage
		if len(w.messages) > 0 {
			msg = w.messages[0]
		}
		writeProtoResponse(rw, &leapmuxv1.GetAgentMessageResponse{Message: msg})
	default:
		_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
	}
}

func TestAgentMessagesOverIPC_NDJSONAndTSVPrimaryListProjection(t *testing.T) {
	id := fullTestID("ag-proj")
	reply := "Hello world"
	baseWorker := func(msgs []*leapmuxv1.AgentChatMessage) *summaryMessagesWorker {
		return &summaryMessagesWorker{
			messages: msgs,
			goal: &leapmuxv1.AgentGoalSnapshot{
				Goal: &leapmuxv1.AgentGoal{Objective: "Ship CLI", Status: leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE},
				SupportedActions: []leapmuxv1.AgentGoalAction{
					leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE,
					leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR,
				},
			},
			todos: &leapmuxv1.AgentTodoSnapshot{
				Todos: []*leapmuxv1.TodoItem{{Id: "td-1", Content: "Fix projection", Status: leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS}},
			},
			tasks: &leapmuxv1.AgentBackgroundTaskSnapshot{
				Tasks: []*leapmuxv1.BackgroundTaskItem{{Id: "bg-1", Title: "go test", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING}},
			},
		}
	}

	t.Run("non-empty messages emit message rows in ndjson and tsv even when todos and tasks exist", func(t *testing.T) {
		w := baseWorker([]*leapmuxv1.AgentChatMessage{
			{Id: "m-1", Seq: 1, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply, Content: []byte(`{"text":"Hello world"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE},
			{Id: "m-2", Seq: 2, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte(`{"text":"Next"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE},
		})
		startAgentWorker(t, id, w)

		ndjsonOut := runControlText(t, "agent", "messages", id, "--format", "ndjson")
		require.Equal(t, cli.ExitOK, ndjsonOut.Code, "stderr: %s", ndjsonOut.Stderr)
		lines := strings.Split(strings.TrimRight(ndjsonOut.Stdout, "\n"), "\n")
		require.Len(t, lines, 2, "ndjson must emit one message per line, got: %s", ndjsonOut.Stdout)
		var first map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
		assert.Equal(t, "m-1", first["id"])
		assert.NotContains(t, first, "messages", "ndjson must unwrap Messages instead of emitting the list wrapper")

		summaryNDJSON := runControlText(t, "agent", "messages", id, "--summary", "--format", "ndjson")
		require.Equal(t, cli.ExitOK, summaryNDJSON.Code, "stderr: %s", summaryNDJSON.Stderr)
		assert.Equal(t, ndjsonOut.Stdout, summaryNDJSON.Stdout)

		tsvOut := runControlText(t, "agent", "messages", id, "--format", "tsv")
		require.Equal(t, cli.ExitOK, tsvOut.Code, "stderr: %s", tsvOut.Stderr)
		tsvLines := strings.Split(strings.TrimRight(tsvOut.Stdout, "\n"), "\n")
		require.Len(t, tsvLines, 3, "tsv must emit header + 2 message rows, got: %s", tsvOut.Stdout)
		assert.Contains(t, tsvLines[1], "m-1")
		assert.Contains(t, tsvLines[2], "m-2")
		assert.NotContains(t, tsvOut.Stdout, "td-1", "tsv must not project Todos")

		// Default JSON omits unrequested summary fields; --summary includes them.
		defaultJSON := runControl(t, "agent", "messages", id).Data(t)
		assert.NotContains(t, defaultJSON, "todos")
		assert.NotContains(t, defaultJSON, "background_tasks")
		assert.NotContains(t, defaultJSON, "goal")

		summaryJSON := runControl(t, "agent", "messages", id, "--summary").Data(t)
		assert.Contains(t, summaryJSON, "todos")
		assert.Contains(t, summaryJSON, "background_tasks")
		goalJSON, ok := summaryJSON["goal"].(map[string]any)
		require.True(t, ok, "summary JSON must include goal object")
		assert.Equal(t, []any{"pause", "clear"}, goalJSON["supported_actions"])

		summaryText := runControlText(t, "agent", "messages", id, "--summary")
		require.Equal(t, cli.ExitOK, summaryText.Code, "stderr: %s", summaryText.Stderr)
		assert.Contains(t, summaryText.Stdout, "goal: Ship CLI [active]")
		assert.Contains(t, summaryText.Stdout, "todos: 0/1 completed")

		// Verify `agent message` text rendering uses formattedAgentMessage.RenderText
		// and requests IncludeReplyText from the Worker.
		singleText := runControlText(t, "agent", "message", id, "1")
		require.Equal(t, cli.ExitOK, singleText.Code, "stderr: %s", singleText.Stderr)
		assert.Equal(t, "#1 agent: Hello world\n", singleText.Stdout)
		w.mu.Lock()
		require.NotNil(t, w.lastGetMsgReq)
		assert.True(t, w.lastGetMsgReq.GetIncludeReplyText(), "agent message must set IncludeReplyText")
		require.NotNil(t, w.lastListReq)
		assert.True(t, w.lastListReq.GetIncludeReplyText(), "agent messages must set IncludeReplyText")
		w.mu.Unlock()

		singleRaw := runControlText(t, "agent", "message", id, "1", "--raw")
		require.Equal(t, cli.ExitOK, singleRaw.Code, "stderr: %s", singleRaw.Stderr)
		assert.Equal(t, "#1 agent: {\"text\":\"Hello world\"}\n", singleRaw.Stdout)
	})

	t.Run("empty messages emit empty output in ndjson and tsv even when todos exist", func(t *testing.T) {
		w := baseWorker(nil)
		startAgentWorker(t, id, w)

		ndjsonOut := runControlText(t, "agent", "messages", id, "--format", "ndjson")
		require.Equal(t, cli.ExitOK, ndjsonOut.Code, "stderr: %s", ndjsonOut.Stderr)
		assert.Empty(t, ndjsonOut.Stdout, "empty messages must emit no ndjson lines")

		tsvOut := runControlText(t, "agent", "messages", id, "--format", "tsv")
		require.Equal(t, cli.ExitOK, tsvOut.Code, "stderr: %s", tsvOut.Stderr)
		assert.Empty(t, tsvOut.Stdout, "empty messages must emit no tsv rows")
	})
}

func TestAgentMessagesOverIPC_PreflightsWorker(t *testing.T) {
	id := fullTestID("ag-preflight")
	hub := &recordingHub{
		getWorkerErr: connect.NewError(connect.CodeNotFound, "worker removed"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: id},
			TileId:      "root-1",
			WorkerId:    "worker-gone",
			WorkspaceId: "ws-1",
		},
	}
	w := &summaryMessagesWorker{}
	startSpawnIPC(t, hub, w)

	o := runControl(t, "agent", "messages", id)
	f := o.requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "worker-gone")
	assert.Contains(t, hub.called(), leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure)
}

func TestNewWorkerWatchTransport_WrapsChannelOpenFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	hubClient, err := control.NewClientOrAnonymous(srv.URL)
	require.NoError(t, err)

	_, err = newWorkerWatchTransport(t.Context(), hubClient, "worker-offline")
	require.Error(t, err)
	var coded *codedRPCError
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, "channel_open_failed", coded.Code)
	assert.Equal(t, cli.CodeWorkerOffline, cli.FromError(err).Code)
}
