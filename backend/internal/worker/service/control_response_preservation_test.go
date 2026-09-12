package service

import (
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlResponseByteCase struct {
	name     string
	original []byte
}

func controlResponseByteCases() []controlResponseByteCase {
	return []controlResponseByteCase{
		{"numeric literals and whitespace", []byte(" \n{\"id\":9007199254740993,\"result\":{\"enabled\":false,\"count\":0,\"text\":\"\"},\"unknown\":7e50}\n")},
		{"string ID", []byte(`{"id":"001","result":null}`)},
		{"malformed JSON", []byte(`{"unfinished":`)},
		{"invalid UTF-8", []byte{0xff, 0xfe, 0, 0x41}},
		{"empty bytes", []byte{}},
		{"nil bytes", nil},
		{"compressed content", []byte(`{"opaque":"` + strings.Repeat("x", 12000) + `"}`)},
	}
}

func TestControlResponseMessagePreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range controlResponseByteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var plan controlResponsePlan
			plan.requestMeta.RequestID = "request-1"
			plan.requestMeta.AgentSessionID = "session-1"
			plan.resolution.Content = tc.original
			content, err := controlResponseMessageContent(plan)
			require.NoError(t, err)
			assert.Equal(t, tc.original, content.Original)
			assert.Equal(t, "session-1", content.AgentSessionID)
		})
	}
}

func TestControlResponseStoragePreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range controlResponseByteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			}))
			var plan controlResponsePlan
			plan.requestMeta.RequestID = "request-1"
			plan.requestMeta.ClaimToken = "claim-1"
			plan.requestMeta.AgentSessionID = "session-1"
			plan.requestMeta.Payload = []byte(`{"unknown":9007199254740993,"enabled":false,"empty":""}`)
			plan.resolution.Content = tc.original
			persistControlResponseForTest(t, svc, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, plan)
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			original, err := msgcodec.Decompress(rows[0].Content, rows[0].ContentCompression)
			require.NoError(t, err)
			assert.Equal(t, string(tc.original), string(original))
			stored := decodeStructuredControlResponse(t, rows[0])
			assert.Equal(t, string(plan.requestMeta.Payload), string(stored.Request))
			assert.Equal(t, "request-1", stored.RequestID)
			assert.Equal(t, "claim-1", stored.ClaimToken)
			assert.Equal(t, "session-1", rows[0].AgentSessionID)
		})
	}
}

func TestControlResponseMessageKeepsTheFullRequestSeparate(t *testing.T) {
	t.Parallel()
	request := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"item/tool/requestUserInput","params":{"unknown":{"count":0,"enabled":false,"text":""},"questions":[{"id":"q","header":"Choose","options":[{"label":"A","description":"First","preview":"  +---+\n  | A |"}]}]}}`)
	var plan controlResponsePlan
	plan.requestMeta.RequestID = "jsonrpc:9007199254740993"
	plan.requestMeta.Payload = request
	plan.resolution.Content = []byte(`{"id":9007199254740993,"result":{"answers":{}}}`)
	content, err := controlResponseMessageContent(plan)
	require.NoError(t, err)
	assert.Equal(t, request, content.Supplemental)
	assert.NotEmpty(t, content.Metadata)
	assert.NotContains(t, string(content.Original), "controlResponse")
}
