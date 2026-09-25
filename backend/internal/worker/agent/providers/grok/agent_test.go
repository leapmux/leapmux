package grok

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// grokTestSession is the session id that the test peer attaches.
const grokTestSession = "session-1"

// newGrokAgent builds a Grok agent over a fake peer that answers each request
// through respond, with Grok's own hooks and a recording sink.
func newGrokAgent(t *testing.T, opts agent.Options, respond func(agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, *agenttest.ControlSink, func() []agenttest.RecordedRequest) {
	t.Helper()
	if respond == nil {
		respond = func(agenttest.RecordedRequest) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = a.configure(opts)
	return a, sink, requests
}

// openingSession answers session/new with sessionID and every other request
// with `{}`, so a test can clear the context.
func openingSession(sessionID string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"` + sessionID + `"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// sessionIDs returns the sessionId of each recorded request.
func sessionIDs(requests []agenttest.RecordedRequest) []string {
	var out []string
	for _, request := range requests {
		sessionID, _ := request.Params["sessionId"].(string)
		out = append(out, sessionID)
	}
	return out
}

// frame encodes one JSON-RPC message that the agent reads.
func frame(t *testing.T, message map[string]any) []byte {
	t.Helper()
	message["jsonrpc"] = "2.0"
	data, err := json.Marshal(message)
	require.NoError(t, err)
	return data
}

// notification encodes one Grok session notification.
func notification(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "_x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

// sessionUpdate encodes one standard ACP session update.
func sessionUpdate(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

// syncPeer returns once the peer recorded every line that the agent wrote
// before the call. The peer reads the lines in order and answers this request
// only after it recorded each line before it, so a test that asserts that a
// line is ABSENT does not pass only because the peer did not read it yet.
func syncPeer(t *testing.T, a *Agent) {
	t.Helper()
	_, err := a.SendRequest("test/sync", nil, 30*time.Second)
	require.NoError(t, err)
}

// requestsFor returns the recorded requests of one method.
func requestsFor(requests []agenttest.RecordedRequest, method string) []agenttest.RecordedRequest {
	var out []agenttest.RecordedRequest
	for _, request := range requests {
		if request.Method == method {
			out = append(out, request)
		}
	}
	return out
}

func TestGrokSteerInputSendsAnInterjection(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("also mention bananas", nil))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 1)
	params := sent[0].Params
	assert.Equal(t, grokTestSession, params["sessionId"])
	assert.Equal(t, "also mention bananas", params["text"])
	assert.NotEmpty(t, params["interjectionId"], "each steer carries its own id")
	assert.NotContains(t, params, "content", "a text steer needs no content blocks")
}

// pngAttachment is a small image that the steer can carry as an image block.
func pngAttachment() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}
}

func TestGrokSteerInputCarriesAnImageAsAContentBlock(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("look at this", []*leapmuxv1.Attachment{pngAttachment()}))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, "look at this", sent[0].Params["text"])
	blocks, ok := sent[0].Params["content"].([]any)
	require.True(t, ok, "the steer states its blocks")
	require.Len(t, blocks, 2)
	assert.Equal(t, map[string]any{"type": "text", "text": "look at this"}, blocks[0])
	assert.Equal(t, "image", blocks[1].(map[string]any)["type"])
}

// Grok's interjection reads its text and its image blocks, and nothing else.
// A text attachment therefore rides inside the text, where the model reads it.
// As a `resource` block, Grok answered `queued` and the model never saw the
// file.
func TestGrokSteerInputInlinesATextAttachment(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("see the log", []*leapmuxv1.Attachment{
		{Filename: "build.log", MimeType: "text/plain", Data: []byte("error: boom\n")},
		pngAttachment(),
	}))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 1)
	want := "see the log\n\n" +
		"----- BEGIN ATTACHED FILE: build.log (text/plain) -----\nerror: boom\n----- END ATTACHED FILE: build.log -----"
	assert.Equal(t, want, sent[0].Params["text"])
	blocks, ok := sent[0].Params["content"].([]any)
	require.True(t, ok)
	var types []string
	for _, block := range blocks {
		types = append(types, block.(map[string]any)["type"].(string))
	}
	assert.Equal(t, []string{"text", "image"}, types, "no block that Grok cannot read")
	assert.Equal(t, want, blocks[0].(map[string]any)["text"], "the text block, which Grok prefers, carries the file too")
}

func TestGrokSteerInputWithOnlyATextAttachmentNeedsNoBlocks(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("", []*leapmuxv1.Attachment{{Filename: "notes.md", MimeType: "text/markdown", Data: []byte("# Notes")}}))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, "----- BEGIN ATTACHED FILE: notes.md (text/markdown) -----\n# Notes\n----- END ATTACHED FILE: notes.md -----", sent[0].Params["text"])
	assert.NotContains(t, sent[0].Params, "content")
}

// The agent refuses an attachment that Grok's interjection cannot read in
// either form, with a message that says so, and the steer sends nothing. The
// reader then sends the file as a message of its own, which a prompt carries
// as a resource that Grok reads.
func TestGrokSteerInputRefusesAnAttachmentThatTheInterjectionCannotCarry(t *testing.T) {
	t.Parallel()
	for name, attachment := range map[string]*leapmuxv1.Attachment{
		"pdf":    {Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")},
		"binary": {Filename: "blob.bin", MimeType: "application/octet-stream", Data: []byte{0x00, 0xff, 0x10}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newGrokAgent(t, agent.Options{}, nil)
			a.SetPromptActiveForTest(true)

			err := a.SteerInput("read this", []*leapmuxv1.Attachment{pngAttachment(), attachment})

			require.Error(t, err)
			assert.Contains(t, err.Error(), attachment.Filename)
			assert.Contains(t, err.Error(), "send it as a new message")
			assert.False(t, errors.Is(err, agent.ErrNoActiveTurn), "the turn still runs")
			syncPeer(t, a)
			assert.Empty(t, requestsFor(requests(), grokInterjectMethod), "a steer that loses a file is not sent")
		})
	}
}

func TestGrokSteerInputRefusesAnIdleSession(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	assert.ErrorIs(t, a.SteerInput("late", nil), agent.ErrNoActiveTurn)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), grokInterjectMethod), "nothing reaches Grok when no turn runs")
}

func TestGrokSteerInputMapsAnEndedTurn(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == grokInterjectMethod {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"no active turn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	assert.ErrorIs(t, a.SteerInput("late", nil), agent.ErrNoActiveTurn)
}

func TestGrokSteerInputReportsAnotherFailure(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == grokInterjectMethod {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"boom"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	err := a.SteerInput("late", nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, agent.ErrNoActiveTurn), "an internal error is not an ended turn")
}

func TestGrokCompactContextSendsTheCompactCommand(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	require.NoError(t, a.CompactContext())

	// The prompt runs detached, so it reaches the peer after the call returns.
	testutil.RequireEventually(t, func() bool { return len(requestsFor(requests(), acp.MethodSessionPrompt)) == 1 })
	prompts := requestsFor(requests(), acp.MethodSessionPrompt)
	blocks, ok := prompts[0].Params["prompt"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	assert.Equal(t, grokCompactCommand, blocks[0].(map[string]any)["text"])
}

func TestGrokSteersEveryTurn(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)

	assert.True(t, a.SupportsSteering(), "Grok steers through its own interjection, which no handshake advertises")
}

// A steer with an image and no text states an empty text and the image block
// alone: a text block would give Grok an empty first text to read.
func TestGrokSteerInputWithOnlyAnImageSendsNoTextBlock(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("", []*leapmuxv1.Attachment{pngAttachment()}))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, "", sent[0].Params["text"])
	blocks, ok := sent[0].Params["content"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	assert.Equal(t, "image", blocks[0].(map[string]any)["type"])
}

// Each steer states an id of its own, so Grok can tell two steers with the
// same text apart.
func TestGrokSteerInputStatesAFreshIDEachTime(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("again", nil))
	require.NoError(t, a.SteerInput("again", nil))
	syncPeer(t, a)

	sent := requestsFor(requests(), grokInterjectMethod)
	require.Len(t, sent, 2)
	assert.NotEqual(t, sent[0].Params["interjectionId"], sent[1].Params["interjectionId"])
}
