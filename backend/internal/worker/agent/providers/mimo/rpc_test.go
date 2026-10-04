package mimo

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiMoPromptPartMarshalJSON(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		part mimoPromptPart
		want string
	}{
		{name: "a text part states its text", part: mimoPromptPart{Type: promptPartText, Text: "hello"},
			want: `{"type":"text","text":"hello"}`},
		{name: "an empty text part still states the field the server requires", part: mimoPromptPart{Type: promptPartText},
			want: `{"type":"text","text":""}`},
		{name: "a file part carries no text field", part: mimoPromptPart{Type: promptPartFile, URL: "data:image/png;base64,AA==", Mime: "image/png", Filename: "a.png", Text: "ignored"},
			want: `{"type":"file","url":"data:image/png;base64,AA==","mime":"image/png","filename":"a.png"}`},
		{name: "a file part without a name omits it", part: mimoPromptPart{Type: promptPartFile, URL: "data:application/pdf;base64,AA==", Mime: "application/pdf"},
			want: `{"type":"file","url":"data:application/pdf;base64,AA==","mime":"application/pdf"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.part)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(raw))
		})
	}
}

func TestSplitModelID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id   string
		want mimoModelRef
		ok   bool
	}{
		{id: "mock/alpha", want: mimoModelRef{ProviderID: "mock", ModelID: "alpha"}, ok: true},
		{id: "openrouter/anthropic/claude", want: mimoModelRef{ProviderID: "openrouter", ModelID: "anthropic/claude"}, ok: true},
		{id: "alpha"},
		{id: "/alpha"},
		{id: "mock/"},
		{id: ""},
	} {
		got, ok := splitModelID(tc.id)
		assert.Equal(t, tc.ok, ok, "id %q", tc.id)
		assert.Equal(t, tc.want, got, "id %q", tc.id)
		if ok {
			assert.Equal(t, tc.id, joinModelID(got), "the join is the inverse of the split")
		}
	}
	assert.Empty(t, joinModelID(mimoModelRef{ProviderID: "mock"}))
	assert.Empty(t, joinModelID(mimoModelRef{ModelID: "alpha"}))
}

func TestPathSegment(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"ses_test", "ses_-ffe5f308baf58ffe5shdXCeZA", "msg_g001a0cf745102001P11hofJYM", "0b5d6a4e-9c0f-4f7e-8a3a-1b2c3d4e5f60"} {
		segment, err := pathSegment("session", id)
		require.NoError(t, err, "id %q", id)
		assert.Equal(t, id, segment)
	}
	for _, id := range []string{"", "a/b", "..", ".", "a%2Fb", "a b", "a?b", "ses_é"} {
		_, err := pathSegment("session", id)
		assert.ErrorContains(t, err, "not a MiMo session id", "id %q", id)
	}
}

// Every request names the agent's directory and carries the credential. The
// server scopes a session to a directory, so a request without the header
// would reach the server's own working directory.
func TestRPCSendsTheDirectoryAndTheCredential(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)

	_, err := a.rpc.health(context.Background())
	require.NoError(t, err)
	session, err := a.rpc.createSession(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ses_created", session.ID)

	for _, request := range server.allRequests() {
		assert.Equal(t, a.workingDir, request.Header.Get(directoryHeader), "%s %s", request.Method, request.Path)
	}
	require.Len(t, server.requestsTo("POST /session"), 1)
}

func TestRPCRefusesAnUnsafeIDWithoutARequest(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	ctx := context.Background()

	_, err := a.rpc.getSession(ctx, "../config")
	assert.ErrorContains(t, err, "not a MiMo session id")
	_, err = a.rpc.message(ctx, testSessionID, "msg/1")
	assert.ErrorContains(t, err, "not a MiMo message id")
	assert.ErrorContains(t, a.rpc.replyPermission(ctx, "per 1", mimoPermissionReplyBody{Reply: "once"}), "not a MiMo permission id")
	assert.ErrorContains(t, a.rpc.rejectQuestion(ctx, "que/1"), "not a MiMo question id")
	assert.ErrorContains(t, a.rpc.replyBashInteractive(ctx, "", mimoBashReplyBody{}), "not a MiMo interactive command id")
	assert.Empty(t, server.allRequests(), "a refused id sends nothing")
}

func TestRPCRoutes(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	ctx := context.Background()

	require.NoError(t, a.rpc.abort(ctx, testSessionID))
	require.NoError(t, a.rpc.summarize(ctx, testSessionID, mimoModelRef{ProviderID: "mock", ModelID: "alpha"}))
	require.NoError(t, a.rpc.command(ctx, testSessionID, mimoCommandRequest{Command: "goal", Arguments: "clear"}))
	require.NoError(t, a.rpc.replyPermission(ctx, "per_1", mimoPermissionReplyBody{Reply: "reject", Message: "no"}))
	require.NoError(t, a.rpc.replyQuestion(ctx, "que_1", [][]string{{"A"}}))
	require.NoError(t, a.rpc.rejectQuestion(ctx, "que_1"))
	require.NoError(t, a.rpc.replyBashInteractive(ctx, "0b5d6a4e-9c0f", mimoBashReplyBody{Output: "no", ExitCode: 1}))
	require.NoError(t, a.rpc.setSkipAll(ctx, true))
	require.NoError(t, a.rpc.setAutoApproveDelete(ctx, false))

	assert.JSONEq(t, `{"providerID":"mock","modelID":"alpha"}`, string(server.requestsTo("POST /session/ses_test/summarize")[0].Body))
	assert.JSONEq(t, `{"command":"goal","arguments":"clear"}`, string(server.requestsTo("POST /session/ses_test/command")[0].Body))
	assert.JSONEq(t, `{"reply":"reject","message":"no"}`, string(server.requestsTo("POST /permission/per_1/reply")[0].Body))
	assert.JSONEq(t, `{"answers":[["A"]]}`, string(server.requestsTo("POST /question/que_1/reply")[0].Body))
	assert.Len(t, server.requestsTo("POST /question/que_1/reject"), 1)
	assert.JSONEq(t, `{"output":"no","exitCode":1}`, string(server.requestsTo("POST /bash-interactive/0b5d6a4e-9c0f/reply")[0].Body))
	assert.JSONEq(t, `{"enabled":true}`, string(server.requestsTo("POST /permission/skip-all")[0].Body))
	assert.JSONEq(t, `{"enabled":false}`, string(server.requestsTo("POST /permission/auto-approve-delete")[0].Body))
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1)
}

func TestRPCReportsAServerRefusal(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("GET /session/ses_gone", http.StatusNotFound, `{"name":"NotFoundError"}`)
	server.respond("GET /session/ses_blank", http.StatusOK, `{"id":"","directory":"/work"}`)
	server.respond("POST /session", http.StatusOK, `{"id":""}`)

	_, err := a.rpc.getSession(context.Background(), "ses_gone")
	assert.True(t, providerkit.IsHTTPStatus(err, http.StatusNotFound))
	_, err = a.rpc.getSession(context.Background(), "ses_blank")
	assert.ErrorContains(t, err, "the session record has no id", "a resume never adopts a session that it cannot address")
	_, err = a.rpc.createSession(context.Background())
	assert.ErrorContains(t, err, "the new session has no id")
}

func TestRPCReadsSessionStatuses(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)

	statuses, err := a.rpc.sessionStatuses(context.Background())
	require.NoError(t, err)
	assert.Empty(t, statuses, "an idle server lists no session")

	server.respond("GET /session/status", http.StatusOK, `{"ses_test":{"type":"retry","attempt":2,"message":"overloaded","next":17}}`)
	statuses, err = a.rpc.sessionStatuses(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]mimoStatus{"ses_test": {Type: "retry", Attempt: 2, Message: "overloaded", Next: 17}}, statuses)
}
