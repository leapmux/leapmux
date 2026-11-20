package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// senderTabID is the agent tab that sends the agent messages of these tests.
var senderTabID = fullTestID("ag-sender")

// queueTextLine returns the line of a text table that starts with prefix,
// after the indent of a table that a "key:" line holds.
func queueTextLine(t *testing.T, text, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return line
		}
	}
	require.Failf(t, "no line starts with the prefix", "prefix %q in:\n%s", prefix, text)
	return ""
}

// agentMessageSupplement is the supplement that the Worker writes on the user
// row of an agent message: the sender, in its protobuf JSON form, under the
// Worker metadata, beside a provider supplement that a reader must skip.
func agentMessageSupplement(t *testing.T, sender string) []byte {
	t.Helper()
	return []byte(`{"provider":{"title":"source"},"metadata":{"` + contracts.MessageMetadataFieldAgentMessageSender + `":` + sender + `}}`)
}

// senderJSON encodes the sender of these tests as the Worker does.
func senderJSON(t *testing.T) string {
	t.Helper()
	encoded, err := protojson.Marshal(&leapmuxv1.AgentMessageSender{TabId: senderTabID, Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE})
	require.NoError(t, err)
	return string(encoded)
}

func TestMessageSenderTextValue(t *testing.T) {
	assert.Equal(t, "agent ag-sende (claude-code)", messageSender{TabID: senderTabID, Provider: "claude-code"}.TextValue())
	assert.Equal(t, "agent ag-sende", messageSender{TabID: senderTabID}.TextValue(), "an unknown provider prints no parentheses")
}

func TestMessageSenderOf(t *testing.T) {
	assert.Nil(t, messageSenderOf(nil), "an input that no agent sent has no sender")
	assert.Nil(t, messageSenderOf(&leapmuxv1.AgentMessageSender{}), "a sender with no tab is no sender")
	assert.Equal(t, &messageSender{TabID: senderTabID, Provider: "claude-code"},
		messageSenderOf(&leapmuxv1.AgentMessageSender{TabId: senderTabID, Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
}

func TestMessageSenderOfRow(t *testing.T) {
	compressed, compression := msgcodec.Compress(agentMessageSupplement(t, senderJSON(t)))
	want := &messageSender{TabID: senderTabID, Provider: "claude-code"}
	none := leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	for _, test := range []struct {
		name        string
		supplement  []byte
		compression leapmuxv1.ContentCompression
		want        *messageSender
	}{
		{name: "a compressed supplement", supplement: compressed, compression: compression, want: want},
		{name: "a plain supplement", supplement: agentMessageSupplement(t, senderJSON(t)), compression: none, want: want},
		{name: "a sender with a field of a later Worker", supplement: agentMessageSupplement(t, `{"tabId":"`+senderTabID+`","provider":"AGENT_PROVIDER_CLAUDE_CODE","title":"later"}`), compression: none, want: want},
		{name: "no supplement", compression: none},
		{name: "metadata without a sender", supplement: []byte(`{"metadata":{"duration_ms":7}}`), compression: none},
		{name: "a provider supplement alone", supplement: []byte(`{"provider":{"x":1}}`), compression: none},
		{name: "a supplement that is no JSON", supplement: []byte(`not json`), compression: none},
		{name: "a sender that is no message", supplement: agentMessageSupplement(t, `"sender-tab"`), compression: none},
		{name: "an unreadable compression", supplement: []byte{1, 2, 3}, compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := messageSenderOfRow(&leapmuxv1.AgentChatMessage{SupplementalContent: test.supplement, SupplementalContentCompression: test.compression})
			assert.Equal(t, test.want, got)
		})
	}
}

func TestAgentMessage_TextLineStatesTheSender(t *testing.T) {
	m := renderAgentMessage(&leapmuxv1.AgentChatMessage{
		Seq: 12, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte(`{"content":"Run the tests"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContent: agentMessageSupplement(t, senderJSON(t)), SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	assert.Equal(t, `#12 user from agent ag-sende (claude-code): {"content":"Run the tests"}`, m.TextLine())
}

// agentMessageInput is a queued agent message from the sender of these tests.
func agentMessageInput(id, text string) *leapmuxv1.QueuedAgentInput {
	item := queuedInput(id, text, 1)
	item.Kind = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE
	item.Sender = &leapmuxv1.AgentMessageSender{TabId: senderTabID, Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}
	return item
}

func TestAgentQueueListOverIPC_PrintsTheSenderOfAnAgentMessage(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(agentMessageInput(inputAlpha, "Run the tests"), queuedInput(inputBeta, "Fix it", 1))
	startAgentWorker(t, id, w)

	items := runControl(t, "agent", "queue", "list", id).Data(t)["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, map[string]any{
		"id": inputAlpha, "kind": "agent_message", "state": "queued", "text": "Run the tests", "reorder_segment": float64(1),
		"sender": map[string]any{"tab_id": senderTabID, "provider": "claude-code"},
	}, items[0], "JSON states the sender's full tab id and its provider")
	assert.NotContains(t, items[1], "sender", "a user message states no sender")

	o := runControlText(t, "agent", "queue", "list", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "KIND           FROM")
	assert.Equal(t, []string{cli.ShortID(inputAlpha), "agent_message", "agent", cli.ShortID(senderTabID), "(claude-code)", "queued", "-", "Run", "the", "tests"},
		strings.Fields(queueTextLine(t, o.Stdout, cli.ShortID(inputAlpha))))
	assert.Equal(t, []string{cli.ShortID(inputBeta), "user_message", "-", "queued", "-", "Fix", "it"},
		strings.Fields(queueTextLine(t, o.Stdout, cli.ShortID(inputBeta))))
}

// messagesWorker is a fake Worker that answers ListAgentMessages with its
// rows and records each request.
type messagesWorker struct {
	rows     []*leapmuxv1.AgentChatMessage
	mu       sync.Mutex
	requests []*leapmuxv1.ListAgentMessagesRequest
}

func (w *messagesWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	in := &leapmuxv1.ListAgentMessagesRequest{}
	if err := proto.Unmarshal(req.GetPayload(), in); err != nil {
		_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	w.mu.Lock()
	w.requests = append(w.requests, in)
	w.mu.Unlock()
	latest := w.rows[len(w.rows)-1].GetSeq()
	writeProtoResponse(rw, &leapmuxv1.ListAgentMessagesResponse{Messages: w.rows, LatestSeq: &latest})
}

func TestAgentMessagesOverIPC_PrintsTheSenderOfAnAgentMessage(t *testing.T) {
	id := fullTestID("ag-2")
	supplement, compression := msgcodec.Compress(agentMessageSupplement(t, senderJSON(t)))
	w := &messagesWorker{rows: []*leapmuxv1.AgentChatMessage{
		{Id: "m-1", Seq: 1, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte(`{"content":"Hello"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE},
		{
			Id: "m-2", Seq: 2, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte(`{"content":"Run the tests"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContent: supplement, SupplementalContentCompression: compression,
		},
	}}
	startAgentWorker(t, id, w)

	messages := runControl(t, "agent", "messages", id).Data(t)["messages"].([]any)
	require.Len(t, messages, 2)
	assert.NotContains(t, messages[0], "sender", "the user's own message states no sender")
	assert.Equal(t, map[string]any{"tab_id": senderTabID, "provider": "claude-code"}, messages[1].(map[string]any)["sender"])

	o := runControlText(t, "agent", "messages", id)
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "#1 user: {\"content\":\"Hello\"}\n#2 user from agent "+cli.ShortID(senderTabID)+" (claude-code): {\"content\":\"Run the tests\"}\n", o.Stdout)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.requests, 2)
	for _, request := range w.requests {
		assert.True(t, proto.Equal(&leapmuxv1.ListAgentMessagesRequest{
			AgentId: id, Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, Limit: contracts.MessagePageLimit,
		}, request), "request: %v", request)
	}
}

func TestAgentMessagesOverIPC_PreservesProviderAndSpanJSON(t *testing.T) {
	const content = `{"messageId":9007199254740993}`
	const spans = `[{"offset":-9007199254740993}]`
	const malformedContent, malformedSpans = "1\n2", "[1] remainder"
	id := fullTestID("agent-transcript")
	worker := &messagesWorker{rows: []*leapmuxv1.AgentChatMessage{
		{
			Id: "m-1", Seq: 1, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			Content: []byte(content), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SpanLines: spans,
		},
		{
			Id: "m-2", Seq: 2, Content: []byte(malformedContent),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, SpanLines: malformedSpans,
		},
		{
			Id: "m-3", Seq: 3, Content: []byte(`"readable note"`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		},
	}}
	startAgentWorker(t, id, worker)

	var envelope struct {
		Data struct {
			Messages []struct {
				Provider  string          `json:"provider"`
				Content   json.RawMessage `json:"content"`
				SpanLines json.RawMessage `json:"span_lines"`
			} `json:"messages"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(runControl(t, "agent", "messages", id).ok(t), &envelope))
	require.Len(t, envelope.Data.Messages, 3)
	first, second := envelope.Data.Messages[0], envelope.Data.Messages[1]
	assert.Equal(t, "claude-code", first.Provider)
	assert.Equal(t, content, string(first.Content))
	assert.Equal(t, spans, string(first.SpanLines))
	var fallbackContent, fallbackSpans string
	require.NoError(t, json.Unmarshal(second.Content, &fallbackContent))
	require.NoError(t, json.Unmarshal(second.SpanLines, &fallbackSpans))
	assert.Equal(t, malformedContent, fallbackContent)
	assert.Equal(t, malformedSpans, fallbackSpans)

	text := runControlText(t, "agent", "messages", id)
	require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
	assert.Contains(t, text.Stdout, content)
	assert.Contains(t, text.Stdout, "readable note\n")
	assert.NotContains(t, text.Stdout, `"readable note"`)
}

func TestAgentSendHelpStatesThatAnAgentSendsAsItself(t *testing.T) {
	o := runControlText(t, "agent", "send", "--help")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "A message that a process in an agent tab sends reaches the recipient as that\nagent's message, not as the user's.")
}
