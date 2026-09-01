package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeInterrupt_IdleIsANoop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)

	require.NoError(t, a.Interrupt())
	assert.Empty(t, stdin.Frames(), "Interrupt with no active turn must not write")
}

func TestZCodeInterrupt_SendsSessionStop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.turnActive = true
	a.mu.Unlock()
	// Nothing answers this stdin, so the call returns a context error. The FRAME
	// is what this test is about.
	a.cancel()

	_ = a.Interrupt()

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, ZCodeMethodSessionStop, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-1", params["sessionId"])
}

func TestZCodeInterrupt_StoppedAgentReturnsError(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.stopped = true
	a.turnActive = true
	a.mu.Unlock()

	err := a.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
	assert.Empty(t, stdin.Frames())
}

func TestZCodeInterrupt_NoSessionIsANoop(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.turnActive = true
	a.sessionID = ""
	a.mu.Unlock()

	require.NoError(t, a.Interrupt())
	assert.Empty(t, stdin.Frames())
}

// "unknown" is the app-server's placeholder before a session exists. Adopting it
// would make later RPCs send a sessionId the app-server rejects.
func TestApplyStateSnapshot_IgnoresTheUnknownSessionPlaceholder(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.applyStateSnapshot(json.RawMessage(`{"session":{"sessionId":"unknown"},"runtime":{"eventSeq":3}}`))

	a.mu.Lock()
	sessionID, seq := a.sessionID, a.lastSeq
	a.mu.Unlock()
	assert.Equal(t, "sess-1", sessionID)
	assert.Equal(t, int64(3), seq)
}

func TestApplyStateSnapshot_EmptyIsANoop(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.applyStateSnapshot(nil)
	a.applyStateSnapshot(json.RawMessage(``))
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	assert.Equal(t, "sess-1", sessionID)
}

// --- opening a session: the resume handle, and the fallbacks when it does not hold ---

func TestZCodeOpenSession_ResumeAdoptsTheSessionAndSkipsCreate(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	// The real startup state: nothing is open yet, so a session id left over from the
	// helper would hide a resume that adopted nothing.
	a.mu.Lock()
	a.sessionID = ""
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionResume,
		`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":42},"projection":{"contextWindow":200000}}`)

	require.NoError(t, a.openSession("sess-old", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "a resume that holds must not also create a session")
	assert.Equal(t, ZCodeMethodSessionResume, requests[0].Method)
	var params struct {
		SessionID string         `json:"sessionId"`
		Workspace zcodeWorkspace `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-old", params.SessionID)
	assert.Equal(t, a.workspace, params.Workspace, "the app-server indexes a session by workspace")

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "sess-old", a.sessionID)
	assert.Equal(t, int64(42), a.lastSeq)
	assert.Equal(t, int64(200000), a.contextWindow)
}

// A resumed session must not replay its history: LeapMux persisted that transcript
// already, and every replayed row would be written a second time.
func TestZCodeOpenSession_ResumedSessionSubscribesAfterTheSnapshotSequence(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.sessionID = ""
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionResume,
		`{"session":{"sessionId":"sess-old"},"runtime":{"eventSeq":42}}`)
	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionSubscribe, `{"eventSeq":42}`)

	require.NoError(t, a.openSession("sess-old", zcodeTestRPCTimeout))
	require.NoError(t, a.subscribe(zcodeTestRPCTimeout))

	var params struct {
		SessionID       string `json:"sessionId"`
		DeliveryKind    string `json:"deliveryKind"`
		IncludeSnapshot bool   `json:"includeSnapshot"`
		AfterSeq        int64  `json:"afterSeq"`
	}
	sub := waitZCodeRequest(t, stdin, ZCodeMethodSessionSubscribe)
	require.NoError(t, json.Unmarshal(sub.Params, &params))
	assert.Equal(t, "sess-old", params.SessionID)
	assert.Equal(t, ZCodeDeliveryContinuous, params.DeliveryKind)
	assert.False(t, params.IncludeSnapshot, "a snapshot is O(messages) and is the main cause of a subscribe timeout")
	assert.Equal(t, int64(42), params.AfterSeq, "the resumed transcript must not be replayed")
}

// The handle comes from a previous run, and the app-server discards a session whose
// store it can no longer read. A refused resume must open a fresh session rather
// than leave the user with an agent that cannot start.
func TestZCodeOpenSession_FallsBackToCreateWhenResumeFails(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.sessionID = ""
	a.mu.Unlock()

	refuseZCodeRequest(t, a, stdin, ZCodeMethodSessionResume, ZCodeErrSessionNotActive, "no such session")
	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionCreate,
		`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0}}`)

	require.NoError(t, a.openSession("sess-gone", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 2)
	assert.Equal(t, ZCodeMethodSessionResume, requests[0].Method, "the resume is attempted first")
	assert.Equal(t, ZCodeMethodSessionCreate, requests[1].Method)
	var create map[string]any
	require.NoError(t, json.Unmarshal(requests[1].Params, &create))
	assert.NotContains(t, create, "sessionId", "session/create mints its own id; the dead handle must not reach it")

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "sess-fresh", a.sessionID)
}

// A resume that answers with no usable session id abandons that document entirely.
//
// "unknown" is the app-server's placeholder for "no session exists", so this reply
// describes no session -- but it still carries a sequence, a mode and a context
// window. Folding any of them into the fresh session below corrupts it: the fresh
// session numbers its events from zero, so the carried sequence becomes a watermark
// that makes dispatchZCodeEvent drop every event under it as a duplicate.
func TestZCodeOpenSession_AbandonedResumeSnapshotIsNotFolded(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.sessionID = ""
	a.mode = contracts.ZCodeDefaultMode
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionResume,
		`{"session":{"sessionId":"unknown"},"runtime":{"eventSeq":9},`+
			`"settings":{"mode":{"current":"yolo"}},"projection":{"contextWindow":123456}}`)
	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionCreate,
		`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0}}`)

	require.NoError(t, a.openSession("sess-gone", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 2)
	assert.Equal(t, ZCodeMethodSessionCreate, requests[1].Method)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "sess-fresh", a.sessionID)
	assert.Zero(t, a.lastSeq, "the abandoned sequence must not become the fresh session's watermark")
	// applyStartupSettings keeps the observed mode wherever the launch asked for none,
	// so a mode folded from the abandoned session would be pinned onto the fresh one.
	assert.Equal(t, contracts.ZCodeDefaultMode, a.mode, "the abandoned session's mode must not survive")
	assert.Zero(t, a.contextWindow, "the abandoned session's context window must not label the fresh session's usage")
}

func TestZCodeOpenSession_WithoutAHandleGoesStraightToCreate(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.sessionID = ""
	a.model = "builtin:zai/GLM-5.3"
	a.mode = contracts.ZCodeModePlan
	a.mu.Unlock()

	answerZCodeRequest(t, a, stdin, ZCodeMethodSessionCreate,
		`{"session":{"sessionId":"sess-new"},"runtime":{"eventSeq":0}}`)

	require.NoError(t, a.openSession("", zcodeTestRPCTimeout))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "an empty handle must not reach session/resume")
	assert.Equal(t, ZCodeMethodSessionCreate, requests[0].Method)
	var params struct {
		Workspace zcodeWorkspace `json:"workspace"`
		Mode      string         `json:"mode"`
		Model     zcodeModelRef  `json:"model"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, a.workspace, params.Workspace)
	assert.Equal(t, contracts.ZCodeModePlan, params.Mode)
	assert.Equal(t, zcodeModelRef{ProviderID: "builtin:zai", ModelID: "GLM-5.3"}, params.Model)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "sess-new", a.sessionID)
}

// A create that produces no session is fatal, unlike a resume that fails: Start
// tears the process down on it, because every later RPC carries the session id.
func TestZCodeOpenSession_ReportsACreateThatProducesNoSession(t *testing.T) {
	t.Parallel()

	t.Run("the app-server refuses", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
		a.mu.Lock()
		a.sessionID = ""
		a.mu.Unlock()

		refuseZCodeRequest(t, a, stdin, ZCodeMethodSessionCreate, ZCodeErrInternal, "the store is unreadable")

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the store is unreadable", "the app-server's reason is the only diagnostic there is")
	})

	t.Run("the reply carries no session id", func(t *testing.T) {
		t.Parallel()
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
		a.mu.Lock()
		a.sessionID = ""
		a.mu.Unlock()

		answerZCodeRequest(t, a, stdin, ZCodeMethodSessionCreate, `{"runtime":{"eventSeq":0}}`)

		err := a.openSession("", zcodeTestRPCTimeout)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ZCodeMethodSessionCreate)
	})
}

func TestZCodeClearContext_OpensAFreshSessionAndDropsPerSessionState(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	sink := &recordingControlSink{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	a.mu.Lock()
	a.turnActive = true
	a.backgroundTurn = true
	a.lastSeq = 99
	a.toolCalls["call-1"] = &zcodeToolCall{name: "Bash", input: json.RawMessage(`{"command":"ls"}`), final: true}
	a.pendingControls["req-1"] = json.RawMessage(`{"type":"control_request"}`)
	a.latestContextUsage = map[string]any{"context_tokens": int64(12)}
	a.mu.Unlock()
	a.children.rememberChild("spawn-1", "sub-1", "child-1")
	a.children.rememberTitle("spawn-2", "a title")

	go func() {
		create := waitZCodeRequest(t, stdin, ZCodeMethodSessionCreate)
		// Every state document carries the settings, and session/create honors the mode
		// it was given -- so the fresh session already runs in the mode the cleared one
		// did, and no session/setMode follows.
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, create), json.RawMessage(
			`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},`+
				`"settings":{"mode":{"current":"build"}}}`)))
		sub := waitZCodeRequest(t, stdin, ZCodeMethodSessionSubscribe)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, sub), json.RawMessage(`{"eventSeq":0}`)))
	}()

	sessionID, ok := a.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "sess-fresh", sessionID)
	for _, req := range stdin.Requests(t) {
		assert.NotEqual(t, ZCodeMethodSetMode, req.Method, "session/create already opened the session in that mode")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.False(t, a.turnActive)
	assert.False(t, a.backgroundTurn)
	assert.Empty(t, a.toolCalls, "one record per call, so a session replace drops every fact about it")
	assert.Empty(t, a.pendingControls)
	assert.Nil(t, a.latestContextUsage)
	// The subagent index goes with the session too. A tool-call id that the replaced
	// session was still running never reaches a result, so an entry left here would
	// route the next session's row into a transcript that belongs to a conversation
	// the user cleared.
	_, hasChild := a.children.child("spawn-1")
	assert.False(t, hasChild)
	_, hasTool := a.children.toolChild("sub-1")
	assert.False(t, hasTool)
	assert.Empty(t, a.children.takeTitle("spawn-2"))
}
