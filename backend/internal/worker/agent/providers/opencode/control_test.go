package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func receiveOpenCodeControlTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("the control operation did not reach its synchronization point")
	}
	var zero T
	return zero
}
func testControlOwner(sink agent.ServiceFacets) openCodeControlOwner {
	services := agent.NewProviderServices(sink)
	services.UpdateSessionID("ses_1")
	return openCodeControlOwner{nativeSessionID: "ses_1", sessionID: "ses_1", sink: services, current: func() bool { return true }}
}
func testPermission(id string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"sessionID":"ses_1","action":"shell","resources":["read"]}`, id))
}
func testNativeForm() json.RawMessage {
	return json.RawMessage(`{"id":"frm_1","sessionID":"ses_1","title":"Choose","metadata":{"kind":"question"},"fields":[{"key":"answer","type":"string"}]}`)
}

func TestOpenCodeControlsUseTheQualifiedToolSpan(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	require.NoError(t, controls.publishPermission(owner, json.RawMessage(`{"id":"per_source","sessionID":"ses_1","action":"shell","resources":[],"source":{"type":"tool","messageID":"msg_source","id":"call_source"}}`)))
	require.NoError(t, controls.publishForm(owner, json.RawMessage(`{"id":"frm_source","sessionID":"ses_1","title":"Choose","metadata":{"kind":"question","tool":{"messageID":"msg_source","id":"call_source"}},"fields":[{"key":"answer","type":"string"}]}`)))
	stream := newOpenCodeToolStreams("ses_1", owner.sink)
	callID := "call_source"
	tool, err := stream.tool("msg_source", &callID)
	require.NoError(t, err)
	published := sink.PublishedControls()
	require.Len(t, published, 2)
	// The Worker refuses each answer to a stored request whose payload gives
	// another key than its publication, so a permission and a form must each
	// give the key that register published them under.
	agenttest.AssertPublishedControlKeys(t, opencodeProvider{}, sink)
	for _, request := range published {
		var payload struct {
			Request struct {
				ToolUseID string `json:"tool_use_id"`
			} `json:"request"`
		}
		require.NoError(t, json.Unmarshal(request.Payload, &payload))
		assert.Equal(t, tool.spanID, payload.Request.ToolUseID)
	}
}

func TestOpenCodeControlsPublishAndAnswerTheExactNativeOwner(t *testing.T) {
	t.Parallel()
	type reply struct {
		path string
		body map[string]any
	}
	calls := make(chan reply, 1)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		calls <- reply{r.URL.Path, body}
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	require.NoError(t, controls.publishPermission(owner, testPermission("per_1")))
	require.NoError(t, controls.publishPermission(owner, testPermission("per_1")))
	published := sink.PublishedControls()
	require.Len(t, published, 1)
	assert.Equal(t, "ses_1", published[0].AgentSessionID)
	assert.NotEmpty(t, published[0].InstanceID)
	require.NoError(t, controls.answer(sink.Answer(published[0].RequestID, []byte(fmt.Sprintf(`{"request_id":%q,"decision":"always","message":"keep this permission"}`, published[0].RequestID)))))
	received := receiveOpenCodeControlTest(t, calls)
	assert.Equal(t, "/api/session/ses_1/permission/per_1/reply", received.path)
	assert.Equal(t, map[string]any{"decision": "always", "message": "keep this permission"}, received.body)
	assert.Equal(t, []string{published[0].RequestID}, sink.CanceledControls())
	assert.Error(t, controls.answer(sink.Answer(published[0].RequestID, []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, published[0].RequestID)))))
}

func TestOpenCodeSendRawInputRefusesUnstatedOrReplacedControlInstance(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	require.NoError(t, controls.publishPermission(owner, testPermission("per_inst")))
	published := sink.LastPublishedControl()
	a := &Agent{controls: controls}
	raw := []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, published.RequestID))

	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw}), agent.ErrControlInstanceUnstated)
	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: "stale-instance"}), agent.ErrControlInstanceReplaced)
	assert.Zero(t, calls.Load())
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: published.InstanceID}))
	assert.Equal(t, int32(1), calls.Load())
}

func TestOpenCodeControlsGlobalFormPreservesZeroFalseAndItsBrokerSession(t *testing.T) {
	t.Parallel()
	calls := make(chan map[string]any, 1)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/session/global/form/frm_1/reply", r.URL.Path)
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		calls <- body
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	owner.nativeSessionID = "global"
	form := json.RawMessage(`{"id":"frm_1","sessionID":"global","title":"Typed form","metadata":{"kind":"mcp-elicitation"},"fields":[{"key":"count","type":"integer"},{"key":"enabled","type":"boolean"}]}`)
	require.NoError(t, controls.publishForm(owner, form))
	published := sink.LastPublishedControl()
	assert.Equal(t, "ses_1", published.AgentSessionID)
	require.NoError(t, controls.answer(sink.Answer(published.RequestID, []byte(fmt.Sprintf(`{"request_id":%q,"answer":{"count":0,"enabled":false}}`, published.RequestID)))))
	assert.Equal(t, map[string]any{"answer": map[string]any{"count": float64(0), "enabled": false}}, receiveOpenCodeControlTest(t, calls))
}

func TestOpenCodeControlsDisablingAutoApprovalPublishesTheUnclaimedPermission(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.Contains(r.URL.Path, "per_first"))
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	controls.setAutoApprove(true)
	assert.True(t, controls.autoApproveEnabled())
	require.NoError(t, controls.publishPermission(owner, testPermission("per_first")))
	receiveOpenCodeControlTest(t, entered)
	require.NoError(t, controls.publishPermission(owner, testPermission("per_second")))
	controls.setAutoApprove(false)
	assert.False(t, controls.autoApproveEnabled())
	releaseOnce.Do(func() { close(release) })
	controls.work.CloseAndWait()
	published := sink.PublishedControls()
	require.Len(t, published, 1)
	assert.Equal(t, openCodeControlKey("permission", "ses_1", "per_second"), published[0].RequestID)
}

type blockedControlPublication struct {
	*agenttest.ControlSink
	entered chan struct{}
	release chan struct{}
}

func (s *blockedControlPublication) PublishControlRequest(request agent.ControlRequest) error {
	close(s.entered)
	<-s.release
	return s.ControlSink.PublishControlRequest(request)
}

func TestOpenCodeControlsCloseWithdrawsALatePublicationOnce(t *testing.T) {
	t.Parallel()
	nativeEntered := make(chan struct{})
	nativeRelease := make(chan struct{})
	var nativeOnce, publishOnce sync.Once
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		close(nativeEntered)
		<-nativeRelease
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	sink := &blockedControlPublication{ControlSink: &agenttest.ControlSink{}, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(controls.close)
	t.Cleanup(func() { nativeOnce.Do(func() { close(nativeRelease) }); publishOnce.Do(func() { close(sink.release) }) })
	owner := testControlOwner(sink)
	controls.setAutoApprove(true)
	assert.True(t, controls.autoApproveEnabled())
	require.NoError(t, controls.publishPermission(owner, testPermission("per_first")))
	receiveOpenCodeControlTest(t, nativeEntered)
	published := make(chan error, 1)
	go func() { published <- controls.publishForm(owner, testNativeForm()) }()
	receiveOpenCodeControlTest(t, sink.entered)
	closed := make(chan struct{})
	go func() { controls.close(); close(closed) }()
	// Keep both operations held until close has retired their records.
	require.Eventually(t, func() bool { controls.mu.Lock(); defer controls.mu.Unlock(); return controls.closed }, 30*time.Second, time.Millisecond)
	publishOnce.Do(func() { close(sink.release) })
	require.NoError(t, receiveOpenCodeControlTest(t, published))
	nativeOnce.Do(func() { close(nativeRelease) })
	receiveOpenCodeControlTest(t, closed)
	canceled := sink.CanceledControls()
	require.Len(t, canceled, 1)
	assert.Equal(t, openCodeControlKey("form", "ses_1", "frm_1"), canceled[0])
}

func TestOpenCodeControlsPublicationFailureRejectsTheNativePermission(t *testing.T) {
	t.Parallel()
	calls := make(chan map[string]any, 1)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		calls <- body
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{PublicationError: assert.AnError}
	owner := testControlOwner(sink)
	require.ErrorIs(t, controls.publishPermission(owner, testPermission("per_1")), assert.AnError)
	body := receiveOpenCodeControlTest(t, calls)
	assert.Equal(t, "reject", body["decision"])
	assert.Empty(t, sink.PublishedControls())
	assert.Empty(t, controls.pending)
}

func TestOpenCodeControlsDefiniteRefusalKeepsTheRequestAvailable(t *testing.T) {
	t.Parallel()
	var callsMu sync.Mutex
	calls := 0
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		callsMu.Lock()
		calls++
		current := calls
		callsMu.Unlock()
		if current == 1 {
			w.WriteHeader(http.StatusBadRequest)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	require.NoError(t, controls.publishPermission(testControlOwner(sink), testPermission("per_1")))
	reqID := sink.LastPublishedControl().RequestID
	raw := []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, reqID))
	err = controls.answer(sink.Answer(reqID, raw))
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.CanceledControls())
	require.NoError(t, controls.answer(sink.Answer(reqID, raw)))
	assert.Len(t, sink.CanceledControls(), 1)
	callsMu.Lock()
	defer callsMu.Unlock()
	assert.Equal(t, 2, calls)
}

func TestOpenCodeControlsUncertainReplyRequiresAnAuthoritativeSnapshot(t *testing.T) {
	t.Parallel()
	var callsMu sync.Mutex
	calls := 0
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	require.NoError(t, controls.publishPermission(testControlOwner(sink), testPermission("per_1")))
	reqID := sink.LastPublishedControl().RequestID
	raw := []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, reqID))
	require.ErrorIs(t, controls.answer(sink.Answer(reqID, raw)), agent.ErrDeliveryUncertain)
	require.ErrorIs(t, controls.answer(sink.Answer(reqID, raw)), agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.CanceledControls())
	callsMu.Lock()
	defer callsMu.Unlock()
	assert.Equal(t, 1, calls)
}

func TestOpenCodeControlsBrokerReplacementCreatesAFreshPublication(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	owner.nativeSessionID = "global"
	raw := json.RawMessage(`{"id":"frm_1","sessionID":"global","title":"Confirm","metadata":{"kind":"mcp-elicitation"},"fields":[{"key":"confirm","type":"boolean"}]}`)
	require.NoError(t, controls.publishForm(owner, raw))
	first := sink.LastPublishedControl()
	owner.sessionID = "ses_replacement"
	owner.sink.UpdateSessionID(owner.sessionID)
	require.NoError(t, controls.publishForm(owner, raw))
	second := sink.LastPublishedControl()
	assert.Equal(t, first.RequestID, second.RequestID)
	assert.NotEqual(t, first.InstanceID, second.InstanceID)
	assert.Equal(t, "ses_replacement", second.AgentSessionID)
	assert.Equal(t, []string{first.RequestID}, sink.CanceledControls())
}

func TestOpenCodeControlsRefuseForeignAndRetiredOwners(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an unowned control must send no HTTP request")
		w.WriteHeader(http.StatusBadRequest)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	owner.nativeSessionID = "ses_other"
	require.Error(t, controls.publishPermission(owner, testPermission("per_1")))
	owner.nativeSessionID = "ses_1"
	owner.current = func() bool { return false }
	require.Error(t, controls.publishPermission(owner, testPermission("per_1")))
	assert.Empty(t, sink.PublishedControls())
}

func TestOpenCodeControlsNativeSettlementWithdrawsOnlyItsRequest(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a native settlement sends no HTTP reply")
		w.WriteHeader(http.StatusBadRequest)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	require.NoError(t, controls.publishPermission(owner, testPermission("per_1")))
	require.NoError(t, controls.publishPermission(owner, testPermission("per_2")))
	controls.retire("permission", "ses_1", "per_1")
	controls.retire("permission", "ses_1", "per_1")
	assert.Equal(t, []string{openCodeControlKey("permission", "ses_1", "per_1")}, sink.CanceledControls())
	controls.mu.Lock()
	defer controls.mu.Unlock()
	assert.Len(t, controls.pending, 1)
	assert.Contains(t, controls.pending, openCodeControlKey("permission", "ses_1", "per_2"))
}

func TestOpenCodeControlsOwnerRetirementPreservesOtherSessionRequests(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("publication retirement sends no native reply")
		w.WriteHeader(http.StatusBadRequest)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	require.NoError(t, controls.publishPermission(owner, testPermission("per_1")))
	child := testControlOwner(sink)
	child.nativeSessionID = "ses_child"
	child.sessionID = "ses_child"
	require.NoError(t, controls.publishPermission(child, json.RawMessage(`{"id":"per_child","sessionID":"ses_child","action":"read","resources":[]}`)))
	controls.retireOwner("ses_1")
	assert.Equal(t, []string{openCodeControlKey("permission", "ses_1", "per_1")}, sink.CanceledControls())
	controls.mu.Lock()
	defer controls.mu.Unlock()
	assert.Len(t, controls.pending, 1)
	assert.Contains(t, controls.pending, openCodeControlKey("permission", "ses_child", "per_child"))
}

func TestOpenCodeControlsRetryUncertainRepliesOnlyAfterNativeReconciliation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"permission", "form", "global form"} {
		for _, settled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/settled=%t", kind, settled), func(t *testing.T) {
				t.Parallel()
				var posts, snapshots atomic.Int32
				nativeID, resource := "per_1", "permission"
				nativeSessionID := "ses_1"
				nativeRaw := testPermission(nativeID)
				if kind != "permission" {
					nativeID, resource = "frm_1", "form"
					nativeRaw = testNativeForm()
				}
				if kind == "global form" {
					nativeSessionID = "global"
					nativeRaw = json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(nativeRaw), `"ses_1"`, `"global"`), `"question"`, `"mcp-elicitation"`))
				}
				rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						snapshots.Add(1)
						assert.Equal(t, "/api/session/"+nativeSessionID+"/"+resource, r.URL.Path)
						if settled {
							_, _ = fmt.Fprint(w, `{"data":[]}`)
						} else {
							_, _ = fmt.Fprintf(w, `{"data":[%s]}`, nativeRaw)
						}
						return
					}
					assert.Equal(t, "/api/session/"+nativeSessionID+"/"+resource+"/"+nativeID+"/reply", r.URL.Path)
					if posts.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				})
				controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
				require.NoError(t, err)
				t.Cleanup(controls.close)
				sink := &agenttest.ControlSink{}
				owner := testControlOwner(sink)
				owner.nativeSessionID = nativeSessionID
				publish := func() error {
					if kind == "permission" {
						return controls.publishPermission(owner, nativeRaw)
					}
					return controls.publishForm(owner, nativeRaw)
				}
				require.NoError(t, publish())
				requestID := sink.LastPublishedControl().RequestID
				answer := []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, requestID))
				if kind != "permission" {
					answer = []byte(fmt.Sprintf(`{"request_id":%q,"answer":{"answer":"done"}}`, requestID))
				}
				require.ErrorIs(t, controls.answer(sink.Answer(requestID, answer)), agent.ErrDeliveryUncertain)
				require.NoError(t, publish(), "duplicate live announcements do not reconcile an uncertain answer")
				assert.Zero(t, snapshots.Load())
				require.NoError(t, controls.reconcilePending(resource, nativeSessionID))
				if !settled {
					publications := sink.PublishedControls()
					require.Len(t, publications, 2)
					assert.NotEqual(t, publications[0].InstanceID, publications[1].InstanceID)
				}
				err = controls.answer(sink.Answer(requestID, answer))
				if settled {
					require.ErrorIs(t, err, agent.ErrControlRequestNotPending)
					assert.EqualValues(t, 1, posts.Load())
				} else {
					require.NoError(t, err)
					assert.EqualValues(t, 2, posts.Load())
				}
				assert.EqualValues(t, 1, snapshots.Load())
				expectedCancellations := []string{requestID}
				if !settled {
					expectedCancellations = append(expectedCancellations, requestID)
				}
				assert.Equal(t, expectedCancellations, sink.CanceledControls())
				require.ErrorIs(t, controls.answer(sink.Answer(requestID, answer)), agent.ErrControlRequestNotPending)
			})
		}
	}
}

type blockedOpenCodeSnapshot struct {
	controls  *openCodeControls
	sink      *agenttest.ControlSink
	answer    []byte
	entered   chan struct{}
	release   func()
	posts     atomic.Int32
	snapshots atomic.Int32
}

func newBlockedOpenCodeSnapshot(t *testing.T, pending bool) *blockedOpenCodeSnapshot {
	t.Helper()
	fixture := &blockedOpenCodeSnapshot{sink: &agenttest.ControlSink{}, entered: make(chan struct{})}
	released := make(chan struct{})
	var releaseOnce, enterOnce sync.Once
	fixture.release = func() { releaseOnce.Do(func() { close(released) }) }
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fixture.snapshots.Add(1)
			enterOnce.Do(func() { close(fixture.entered) })
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
			if pending {
				_, _ = fmt.Fprintf(w, `{"data":[%s]}`, testPermission("per_1"))
			} else {
				_, _ = fmt.Fprint(w, `{"data":[]}`)
			}
			return
		}
		if fixture.posts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	fixture.controls = controls
	t.Cleanup(controls.close)
	t.Cleanup(fixture.release)
	require.NoError(t, controls.publishPermission(testControlOwner(fixture.sink), testPermission("per_1")))
	fixture.answer = []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, fixture.sink.LastPublishedControl().RequestID))
	return fixture
}

func TestOpenCodeControlsSnapshotCannotRecreateASettledRequest(t *testing.T) {
	t.Parallel()
	fixture := newBlockedOpenCodeSnapshot(t, true)
	requestID := fixture.sink.LastPublishedControl().RequestID
	require.ErrorIs(t, fixture.controls.answer(fixture.sink.Answer(requestID, fixture.answer)), agent.ErrDeliveryUncertain)
	finished := make(chan error, 1)
	go func() { finished <- fixture.controls.reconcilePending("permission", "ses_1") }()
	receiveOpenCodeControlTest(t, fixture.entered)
	fixture.controls.retire("permission", "ses_1", "per_1")
	fixture.release()
	require.NoError(t, receiveOpenCodeControlTest(t, finished))
	assert.Len(t, fixture.sink.PublishedControls(), 1)
	assert.Len(t, fixture.sink.CanceledControls(), 1)
	require.ErrorIs(t, fixture.controls.answer(fixture.sink.Answer(requestID, fixture.answer)), agent.ErrControlRequestNotPending)
	assert.EqualValues(t, 1, fixture.posts.Load())
}

func TestOpenCodeControlsSnapshotCannotOverwriteANewerReplyAttempt(t *testing.T) {
	t.Parallel()
	fixture := newBlockedOpenCodeSnapshot(t, false)
	requestID := fixture.sink.LastPublishedControl().RequestID
	finished := make(chan error, 1)
	go func() { finished <- fixture.controls.reconcilePending("permission", "ses_1") }()
	receiveOpenCodeControlTest(t, fixture.entered)
	require.ErrorIs(t, fixture.controls.answer(fixture.sink.Answer(requestID, fixture.answer)), agent.ErrDeliveryUncertain)
	fixture.release()
	require.NoError(t, receiveOpenCodeControlTest(t, finished))
	assert.Empty(t, fixture.sink.CanceledControls())
	assert.Len(t, fixture.sink.PublishedControls(), 1)
	require.ErrorIs(t, fixture.controls.answer(fixture.sink.Answer(requestID, fixture.answer)), agent.ErrDeliveryUncertain)
	assert.EqualValues(t, 1, fixture.posts.Load())
}

func TestOpenCodeControlsSnapshotCannotReplaceANewerPublicationOwner(t *testing.T) {
	t.Parallel()
	fixture := newBlockedOpenCodeSnapshot(t, true)
	requestID := fixture.sink.LastPublishedControl().RequestID
	require.ErrorIs(t, fixture.controls.answer(fixture.sink.Answer(requestID, fixture.answer)), agent.ErrDeliveryUncertain)
	finished := make(chan error, 1)
	go func() { finished <- fixture.controls.reconcilePending("permission", "ses_1") }()
	receiveOpenCodeControlTest(t, fixture.entered)
	owner := testControlOwner(fixture.sink)
	owner.sessionID = "ses_replacement"
	require.NoError(t, fixture.controls.publishPermission(owner, testPermission("per_1")))
	replacement := fixture.sink.LastPublishedControl()
	fixture.release()
	require.NoError(t, receiveOpenCodeControlTest(t, finished))
	assert.Len(t, fixture.sink.PublishedControls(), 2)
	assert.Equal(t, replacement.InstanceID, fixture.sink.LastPublishedControl().InstanceID)
	require.NoError(t, fixture.controls.answer(fixture.sink.Answer(replacement.RequestID, fixture.answer)))
	assert.EqualValues(t, 2, fixture.posts.Load())
}

func TestOpenCodeControlsAutoApprovalUsesLinearOwnerChecks(t *testing.T) {
	t.Parallel()
	const count = 1000
	requests := make(chan string, count)
	var forms atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if strings.Contains(r.URL.Path, "/permission/") {
			assert.Equal(t, "once", body["decision"])
			parts := strings.Split(r.URL.Path, "/")
			requests <- parts[len(parts)-2]
		} else {
			forms.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &agenttest.ControlSink{}
	owner := testControlOwner(sink)
	var checks atomic.Int64
	owner.current = func() bool { checks.Add(1); return true }
	publishForm := func(id string) {
		require.NoError(t, controls.publishForm(owner, json.RawMessage(strings.ReplaceAll(string(testNativeForm()), "frm_1", id))))
	}
	publishForm("frm_before")
	for i := range count {
		require.NoError(t, controls.publishPermission(owner, testPermission(fmt.Sprintf("per_%04d", i))))
		if i == count/2 {
			publishForm("frm_middle")
		}
	}
	checks.Store(0)
	controls.setAutoApprove(true)
	for i := range count {
		assert.Equal(t, fmt.Sprintf("per_%04d", i), receiveOpenCodeControlTest(t, requests))
	}
	controls.work.CloseAndWait()
	assert.LessOrEqual(t, checks.Load(), int64(4*count), "permission approval must not rescan every remaining owner after each reply")
	assert.Zero(t, forms.Load(), "Auto-approve cannot answer a form")
	requestID := openCodeControlKey("form", "ses_1", "frm_before")
	require.NoError(t, controls.answer(sink.Answer(requestID, []byte(fmt.Sprintf(`{"request_id":%q,"answer":{"answer":"chosen"}}`, requestID)))))
	assert.EqualValues(t, 1, forms.Load())
}

func TestOpenCodeControlsAutoApprovalRevalidatesItsBatch(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"add", "retire", "disable"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			entered, released := make(chan struct{}), make(chan struct{})
			requests := make(chan string, 3)
			var enterOnce, releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(released) }) }
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				parts := strings.Split(r.URL.Path, "/")
				id := parts[len(parts)-2]
				requests <- id
				if id == "per_1" {
					enterOnce.Do(func() { close(entered) })
					select {
					case <-released:
					case <-r.Context().Done():
						return
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})
			controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
			require.NoError(t, err)
			t.Cleanup(controls.close)
			t.Cleanup(release)
			sink := &agenttest.ControlSink{}
			owner := testControlOwner(sink)
			require.NoError(t, controls.publishPermission(owner, testPermission("per_1")))
			if action != "add" {
				require.NoError(t, controls.publishPermission(owner, testPermission("per_2")))
			}
			controls.setAutoApprove(true)
			receiveOpenCodeControlTest(t, entered)
			switch action {
			case "add":
				require.NoError(t, controls.publishPermission(owner, testPermission("per_2")))
			case "retire":
				controls.retire("permission", "ses_1", "per_2")
			case "disable":
				controls.setAutoApprove(false)
			}
			release()
			assert.Equal(t, "per_1", receiveOpenCodeControlTest(t, requests))
			if action == "add" {
				assert.Equal(t, "per_2", receiveOpenCodeControlTest(t, requests))
			}
			controls.work.CloseAndWait()
			select {
			case extra := <-requests:
				t.Errorf("unexpected automatic reply for %s", extra)
			default:
			}
			requestID := openCodeControlKey("permission", "ses_1", "per_2")
			answer := []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, requestID))
			if action == "disable" {
				require.NoError(t, controls.answer(sink.Answer(requestID, answer)))
				assert.Equal(t, "per_2", receiveOpenCodeControlTest(t, requests))
			} else {
				require.ErrorIs(t, controls.answer(sink.Answer(requestID, answer)), agent.ErrControlRequestNotPending)
			}
		})
	}
}

func TestOpenCodeControlsNotifyRecoveryAfterRecordingUncertainty(t *testing.T) {
	t.Parallel()
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%t", automatic), func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
			var controls *openCodeControls
			type observation struct{ present, uncertain, answering, automaticAttempted bool }
			var observedMu sync.Mutex
			var observations []observation
			notify := func() {
				controls.mu.Lock()
				record := controls.pending[openCodeControlKey("permission", "ses_1", "per_1")]
				value := observation{present: record != nil}
				if record != nil {
					value.uncertain = record.uncertain
					value.answering = record.answering
					value.automaticAttempted = record.automaticAttempted
				}
				controls.mu.Unlock()
				observedMu.Lock()
				observations = append(observations, value)
				observedMu.Unlock()
			}
			var err error
			controls, err = newOpenCodeControls(t.Context(), rpc, notify)
			require.NoError(t, err)
			t.Cleanup(controls.close)
			sink := &agenttest.ControlSink{}
			require.NoError(t, controls.publishPermission(testControlOwner(sink), testPermission("per_1")))
			if automatic {
				controls.setAutoApprove(true)
				controls.work.CloseAndWait()
			} else {
				published := sink.LastPublishedControl()
				require.ErrorIs(t, controls.answer(sink.Answer(published.RequestID, []byte(fmt.Sprintf(`{"request_id":%q,"decision":"once"}`, published.RequestID)))), agent.ErrDeliveryUncertain)
			}
			observedMu.Lock()
			defer observedMu.Unlock()
			require.Len(t, observations, 1)
			assert.True(t, observations[0].present)
			assert.True(t, observations[0].uncertain)
			assert.False(t, observations[0].answering)
			assert.Equal(t, automatic, observations[0].automaticAttempted)
		})
	}
}

func TestOpenCodeControlsRequireRecoveryNotification(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	_, err := newOpenCodeControls(t.Context(), rpc, nil)
	require.Error(t, err)
}

func TestOpenCodeControlsOwnerRetirementWithdrawsALatePublicationOnce(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("retiring a publication must not answer the native form")
		w.WriteHeader(http.StatusInternalServerError)
	})
	controls, err := newOpenCodeControls(t.Context(), rpc, func() {})
	require.NoError(t, err)
	t.Cleanup(controls.close)
	sink := &blockedControlPublication{ControlSink: &agenttest.ControlSink{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(sink.release) }) }
	t.Cleanup(release)
	owner := testControlOwner(sink)
	finished := make(chan error, 1)
	go func() { finished <- controls.publishForm(owner, testNativeForm()) }()
	receiveOpenCodeControlTest(t, sink.entered)
	controls.retireOwner(owner.sessionID)
	controls.retireOwner(owner.sessionID)
	assert.Empty(t, sink.CanceledControls())
	release()
	require.NoError(t, receiveOpenCodeControlTest(t, finished))
	controls.retireOwner(owner.sessionID)
	controls.close()
	assert.Equal(t, []string{openCodeControlKey("form", "ses_1", "frm_1")}, sink.CanceledControls())
}
