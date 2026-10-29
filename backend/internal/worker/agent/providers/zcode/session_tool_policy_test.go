package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// zcodeExpectedToolDenylist states the tool policy independently of the request
// builders: the complete Cron family, in the order the schema states it.
func zcodeExpectedToolDenylist() []string {
	return []string{
		contracts.ZCodeToolNameCronCreate,
		contracts.ZCodeToolNameCronList,
		contracts.ZCodeToolNameCronUpdate,
		contracts.ZCodeToolNameCronDelete,
	}
}

// zcodeAssertToolDenylist asserts the complete four-tool set by name, then the
// exact list: a policy that drops one name leaves that tool visible.
func zcodeAssertToolDenylist(t *testing.T, got []string) {
	t.Helper()
	assert.Contains(t, got, contracts.ZCodeToolNameCronCreate)
	assert.Contains(t, got, contracts.ZCodeToolNameCronList)
	assert.Contains(t, got, contracts.ZCodeToolNameCronUpdate)
	assert.Contains(t, got, contracts.ZCodeToolNameCronDelete)
	assert.Equal(t, zcodeExpectedToolDenylist(), got, "the policy is exactly the Cron tool family")
}

// zcodeToolPolicyParams is the create/resume parameter shape the policy tests
// read.
type zcodeToolPolicyParams struct {
	SessionID    string         `json:"sessionId"`
	Workspace    zcodeWorkspace `json:"workspace"`
	ToolDenylist []string       `json:"toolDenylist"`
}

// refuseZCodeToolDenylist answers the next request for method like an
// app-server whose schema predates the field: a request that states the tool
// policy is refused with the schema error, and a request that omits the field
// -- a forbidden retry -- is answered with a session. The test then fails on
// the adoption or on the request count, never on an unanswered RPC.
func refuseZCodeToolDenylist(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, method, permissiveReply string) {
	t.Helper()
	go func() {
		req := waitZCodeRequest(t, stdin, method)
		var params map[string]json.RawMessage
		if json.Unmarshal(req.Params, &params) == nil {
			if _, stated := params["toolDenylist"]; stated {
				a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), -32602, `Unrecognized key: "toolDenylist"`))
				return
			}
		}
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(permissiveReply)))
	}()
}

// Every session/create states the tool policy: LeapMux answers none of ZCode's
// Cron controls, so the family is hidden for the whole session instead of
// staying visible and unanswerable.
func TestZCodeToolDenylist_CreateStatesTheCronPolicy(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionCreate,
		`{"session":{"sessionId":"sess-new"},"runtime":{"eventSeq":0}}`)
	require.NoError(t, a.openSession("", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionCreate, requests[0].Method)
	var params zcodeToolPolicyParams
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	zcodeAssertToolDenylist(t, params.ToolDenylist)
}

// A startup resume states the same policy as the create it replaces.
func TestZCodeToolDenylist_StartupResumeStatesTheCronPolicy(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":0}}`)
	require.NoError(t, a.openSession("sess-old", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionResume, requests[0].Method)
	var params zcodeToolPolicyParams
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-old", params.SessionID)
	assert.Equal(t, a.workspace, params.Workspace, "the app-server indexes a session by workspace")
	zcodeAssertToolDenylist(t, params.ToolDenylist)
}

// The catalog refresh resumes through the shared builder, so its resume states
// the same workspace and the same tool policy as every other resume.
func TestZCodeToolDenylist_CatalogResumeStatesTheCronPolicy(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)

	answerZCodeRequest(t, a, stdin, MethodSessionResume,
		`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[]}}}`)
	changed, err := a.readZCodeModelCatalog()
	require.NoError(t, err)
	assert.True(t, changed)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionResume, requests[0].Method)
	var params zcodeToolPolicyParams
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-1", params.SessionID)
	assert.Equal(t, a.workspace, params.Workspace, "the catalog resume states the same workspace as every resume")
	zcodeAssertToolDenylist(t, params.ToolDenylist)
}

// A server whose schema predates toolDenylist refuses the request. LeapMux
// returns that native error and opens no session: a retry without the field
// would silently drop the tool policy that the caller asked for.
func TestZCodeToolDenylist_ServerRejectionFailsWithoutASecondRequestOrAdoption(t *testing.T) {
	t.Parallel()

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		refuseZCodeToolDenylist(t, a, stdin, MethodSessionCreate,
			`{"session":{"sessionId":"sess-fallback"},"runtime":{"eventSeq":0}}`)

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Unrecognized key: "toolDenylist"`,
			"the app-server's reason is the only diagnostic there is")
		code, ok := zcodeErrorCode(err)
		assert.True(t, ok, "the native error must survive")
		assert.Equal(t, -32602, code)

		requests := stdin.Requests(t)
		require.Len(t, requests, 1, "a rejected toolDenylist must not be retried without the field")
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Empty(t, a.sessionID, "a rejected request creates no session, so the agent adopts none")
	})

	t.Run("startup resume", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		refuseZCodeToolDenylist(t, a, stdin, MethodSessionResume,
			`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":0}}`)

		err := a.openSession("sess-old", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sess-old", "the handle that failed is what the user has to replace")
		assert.Contains(t, err.Error(), "/clear", "the resume-failure wrapping is preserved")
		assert.Contains(t, err.Error(), `Unrecognized key: "toolDenylist"`,
			"the native reason survives the wrapping")
		code, ok := zcodeErrorCode(err)
		assert.True(t, ok, "the native error must survive the wrapping")
		assert.Equal(t, -32602, code)

		requests := stdin.Requests(t)
		require.Len(t, requests, 1, "a rejected toolDenylist must not be retried without the field")
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Empty(t, a.sessionID, "a refused resume adopts no session")
	})

	t.Run("catalog resume", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)

		refuseZCodeToolDenylist(t, a, stdin, MethodSessionResume,
			`{"session":{"sessionId":"sess-1"},"settings":{"model":{"available":[]}}}`)

		changed, err := a.readZCodeModelCatalog()
		require.Error(t, err)
		assert.False(t, changed)
		assert.Contains(t, err.Error(), `Unrecognized key: "toolDenylist"`,
			"the app-server's reason is the only diagnostic there is")
		code, ok := zcodeErrorCode(err)
		assert.True(t, ok, "the native error must survive")
		assert.Equal(t, -32602, code)

		requests := stdin.Requests(t)
		require.Len(t, requests, 1, "a rejected toolDenylist must not be retried without the field")
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Equal(t, "sess-1", a.sessionID, "a failed catalog read changes no session")
	})
}

// An error that says nothing about the tool policy must not grow a retry
// either: one request, one failure, no adoption.
func TestZCodeOpenSession_UnrelatedErrorMakesOneRequest(t *testing.T) {
	t.Parallel()

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		refuseZCodeRequest(t, a, stdin, MethodSessionCreate, ErrInternal, "the store is unreadable")

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the store is unreadable")

		requests := stdin.Requests(t)
		require.Len(t, requests, 1)
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Empty(t, a.sessionID)
	})

	t.Run("startup resume", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
		a.Mu.Lock()
		a.sessionID = ""
		a.Mu.Unlock()

		refuseZCodeRequest(t, a, stdin, MethodSessionResume, ErrInternal, "the store is unreadable")

		err := a.openSession("sess-old", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the store is unreadable")

		requests := stdin.Requests(t)
		require.Len(t, requests, 1)
		a.Mu.Lock()
		defer a.Mu.Unlock()
		assert.Empty(t, a.sessionID)
	})
}

// toolDenylist is a session policy of session/create and session/resume.
// session/send carries it nowhere: there the field would take its per-turn
// meaning, which differs from this one.
func TestZCodeToolDenylist_StaysOffSessionSend(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "sess-1"
	a.model = "provider/model"
	a.Mu.Unlock()

	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"sessionId":"sess-1","accepted":true,"stateRevision":0}`)
	require.NoError(t, a.SendInput("guide", nil))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionSend, requests[0].Method)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.NotContains(t, params, "toolDenylist", "its per-turn meaning differs from the session policy")
}
