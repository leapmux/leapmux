package agenttest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// replyTextCase is one row of testdata/reply_text_conformance.json: a stored
// agent row of one provider, and the reply text that the browser's transcript
// shows for it. A null reply is a row that holds no reply.
type replyTextCase struct {
	Name           string          `json:"name"`
	Provider       string          `json:"provider"`
	Content        json.RawMessage `json:"content"`
	Supplemental   json.RawMessage `json:"supplemental"`
	AgentSessionID string          `json:"agent_session_id"`
	AssembledKind  string          `json:"assembled_kind"`
	Reply          *string         `json:"reply"`
}

func loadReplyTextConformance(t *testing.T) []replyTextCase {
	t.Helper()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "reply_text_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []replyTextCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		_, known := leapmuxv1.AgentProvider_value[tc.Provider]
		require.Truef(t, known, "case %q states the unknown provider %q", tc.Name, tc.Provider)
	}
	return fixture.Cases
}

// RunReplyTextConformance replays the cases of registration's provider in
// testdata/reply_text_conformance.json through agent.ReplyText with the
// provider's plugin. The browser replays the same file through its row
// extraction, so the reply text that `leapmux control agent send --wait`
// prints is the text that the transcript shows.
//
// The provider must have a case with a reply and a case without one, so
// neither half of its answer goes unchecked. A case that states an
// assembled kind must be a row that the Worker stores with that kind.
func RunReplyTextConformance(t *testing.T, registration agent.Registration) {
	t.Helper()
	var replies, others int
	for _, tc := range loadReplyTextConformance(t) {
		if tc.Provider != registration.Provider.String() {
			continue
		}
		if tc.Reply != nil {
			replies++
		} else {
			others++
		}
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			kind, _ := agent.MessageMetadata(agent.MessageContent{Original: tc.Content})
			assert.Equal(t, assembledKindToken(tc.AssembledKind), kind,
				"the case states the assembled kind that the Worker stores for its content")
			content, err := agent.DecodeMessageSupplement(tc.Content, tc.Supplemental)
			require.NoError(t, err)
			content.AgentSessionID = tc.AgentSessionID
			text, ok := agent.ReplyText(registration.Plugin, content)
			if tc.Reply == nil {
				assert.False(t, ok, "the row holds no reply, but the Worker read %q", text)
				return
			}
			require.True(t, ok, "the row holds a reply that the Worker did not read")
			assert.Equal(t, *tc.Reply, text)
		})
	}
	assert.Positivef(t, replies, "testdata/reply_text_conformance.json has no case with a reply for %v", registration.Provider)
	assert.Positivef(t, others, "testdata/reply_text_conformance.json has no case without a reply for %v", registration.Provider)
}

func assembledKindToken(token string) leapmuxv1.AssembledMessageKind {
	if token == "" {
		return leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED
	}
	if kind := agent.AssembledMessageKindFromToken(token); kind != leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED {
		return kind
	}
	panic("unknown assembled kind " + token)
}
