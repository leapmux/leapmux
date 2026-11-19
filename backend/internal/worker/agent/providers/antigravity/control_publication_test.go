package antigravity

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type interactionPublicationSink struct {
	*agenttest.ControlSink
	fail    atomic.Bool
	entered chan struct{}
	release <-chan struct{}
	// cancelEntered and cancelRelease hold each cancellation of a request
	// instance before the sink applies it, as entered and release hold each
	// publication.
	cancelEntered chan struct{}
	cancelRelease <-chan struct{}
	stored        chan agent.ControlRequest
	mu            sync.Mutex
	active        map[string]string
}

func (s *interactionPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	if s.fail.Load() {
		return errors.New("control storage is unavailable")
	}
	if s.entered != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	if err := s.ControlSink.PublishControlRequest(request); err != nil {
		return err
	}
	s.mu.Lock()
	s.active[request.RequestID] = request.InstanceID
	s.mu.Unlock()
	if s.stored != nil {
		s.stored <- request
	}
	return nil
}

func (s *interactionPublicationSink) CancelControlRequestInstance(requestID, instanceID string) {
	if s.cancelEntered != nil {
		s.cancelEntered <- struct{}{}
		<-s.cancelRelease
	}
	s.mu.Lock()
	if s.active[requestID] == instanceID {
		delete(s.active, requestID)
	}
	s.mu.Unlock()
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func publicationTestAgent(t *testing.T, clock quartz.Clock) (*Agent, *interactionPublicationSink) {
	t.Helper()
	sink := &interactionPublicationSink{
		ControlSink: &agenttest.ControlSink{}, active: make(map[string]string), stored: make(chan agent.ControlRequest, 8),
	}
	a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), clock)
	t.Cleanup(a.streamCancel)
	a.installRoot("cascade", false)
	a.root.trajectoryID = "trajectory"
	return a, sink
}

func waitingInteractionStep(kind string) json.RawMessage {
	interaction := `{"permission":{"resource":{"action":"command","target":"echo marker"}}}`
	if kind == "question" {
		interaction = `{"askQuestion":{"questions":[{"question":"Continue?","options":[]}]}}`
	}
	return waitingStep(interaction, 0)
}

// waitingStep is a step that waits for an answer to the interaction, after it
// completed the given number of earlier interactions.
func waitingStep(interaction string, completed int) json.RawMessage {
	completedInteractions := make([]any, completed)
	for i := range completedInteractions {
		completedInteractions[i] = map[string]any{"request": map[string]any{}, "response": map[string]any{}}
	}
	raw, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusWaiting,
		"metadata":              map[string]any{"toolCall": map[string]any{"id": "waiting-call", "name": "run_command"}},
		"requestedInteraction":  json.RawMessage(interaction),
		"completedInteractions": completedInteractions,
	})
	return raw
}

// recordedLanguageServer is a fake language server that accepts each answer
// and each cancellation, and records them.
type recordedLanguageServer struct {
	mu      sync.Mutex
	answers []json.RawMessage
	cancels int
}

func newRecordedLanguageServer(t *testing.T) (antigravityRPC, *recordedLanguageServer) {
	t.Helper()
	server := &recordedLanguageServer{}
	rpc := testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, languageServerService) {
		case "HandleCascadeUserInteraction":
			var request struct {
				Interaction json.RawMessage `json:"interaction"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			server.mu.Lock()
			server.answers = append(server.answers, request.Interaction)
			server.mu.Unlock()
		case "CancelCascadeInvocation":
			server.mu.Lock()
			server.cancels++
			server.mu.Unlock()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	return rpc, server
}

// answered is the interaction of each answer, in arrival order.
func (s *recordedLanguageServer) answered() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.answers...)
}

func (s *recordedLanguageServer) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancels
}

// An answer states the trajectory of its step, and only a frame of the
// cascade's stream states that trajectory. A waiting step that arrives before
// any frame stated it has no answer that LeapMux can address. So the Worker
// reports the fault and cancels the cascade, instead of publishing a request
// that no answer can reach.
func TestAntigravityControlPublicationRefusesAStepBeforeItsTrajectory(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.root.trajectoryID = ""
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	assert.Zero(t, sink.PublishedControlCount(), "no answer could reach the step")
	assert.Equal(t, 1, server.cancelCount(), "the cascade stops waiting for an answer that never comes")
	require.Len(t, sink.Notifications(), 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0][contracts.NotificationFieldType])
	assert.Contains(t, sink.Notifications()[0][contracts.NotificationFieldText], "trajectory")
	a.Mu.Lock()
	assert.Empty(t, a.root.controls)
	a.Mu.Unlock()
}

// A frame applies the trajectory that it states before its steps, so a
// waiting step in the frame that first states the trajectory is addressed to
// that trajectory.
func TestAntigravityControlPublicationAddressesTheTrajectoryOfItsFrame(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	a.Mu.Lock()
	a.root.trajectoryID = ""
	a.Mu.Unlock()
	update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")})
	update.TrajectoryID = "stated"
	feedUpdate(t, a, update)
	require.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, "antigravity:cascade:stated:0", sink.LastPublishedControl().RequestID)
	agenttest.AssertPublishedControlKeys(t, antigravityProvider{}, sink.ControlSink)
	assert.Empty(t, sink.Notifications())
}

// An answer states the trajectory of its request, so a request on an earlier
// trajectory answers nothing on the current one. A step that waits on a new
// trajectory gets a new request that states the new trajectory.
func TestAntigravityControlPublicationFollowsANewTrajectory(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	step := []json.RawMessage{waitingInteractionStep("permission")}
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, step))
	require.Equal(t, 1, sink.PublishedControlCount())
	update := updateWithSteps(cascadeStatusRunning, []int{0}, step)
	update.TrajectoryID = "next-trajectory"
	feedUpdate(t, a, update)
	require.Equal(t, 2, sink.PublishedControlCount())
	assert.Equal(t, "antigravity:cascade:next-trajectory:0", sink.LastPublishedControl().RequestID)
	assert.Equal(t, []string{"antigravity:cascade:trajectory:0"}, sink.CanceledControls())
}

// A step can request another interaction after LeapMux registered its first
// one: the language server keeps each answered interaction of a step in its
// `completedInteractions`. The request of the first interaction then answers
// nothing that the language server waits for. So the Worker withdraws that
// request instance and publishes the step's current interaction as a new
// instance of the step's request.
func TestAntigravityControlPublicationReplacesAnotherInteraction(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	require.Equal(t, 1, sink.PublishedControlCount())
	first := sink.LastPublishedControl()

	question := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")})
	feedUpdate(t, a, question)
	require.Equal(t, 2, sink.PublishedControlCount(), "the step's current interaction is a new request")
	second := sink.LastPublishedControl()
	assert.Equal(t, first.RequestID, second.RequestID, "the request keeps the address of its step")
	assert.NotEqual(t, first.InstanceID, second.InstanceID)
	assert.Equal(t, []string{first.RequestID}, sink.CanceledControls(), "the first interaction's request is withdrawn")
	sink.mu.Lock()
	assert.Equal(t, map[string]string{second.RequestID: second.InstanceID}, sink.active)
	sink.mu.Unlock()
	assert.NotNil(t, agenttest.ControlPromptOf(antigravityProvider{}, second.Payload).GetQuestion())
	agenttest.AssertPublishedControlKeys(t, antigravityProvider{}, sink.ControlSink)

	feedUpdate(t, a, question)
	assert.Equal(t, 2, sink.PublishedControlCount(), "a repeated frame of the same interaction keeps its request")
	assert.Len(t, sink.CanceledControls(), 1)
}

// A frame that states the step's interaction again in another form, with its
// fields in another order, states the same interaction. The step keeps its
// request instance: a replacement would withdraw the card that the user
// reads, and refuse an answer that the user already gave to it.
func TestAntigravityControlPublicationKeepsAnInteractionThatAFrameStatesInAnotherOrder(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{
		waitingStep(`{"permission":{"resource":{"action":"command","target":"echo marker"},"actionDescription":"Run echo"}}`, 0),
	}))
	require.Equal(t, 1, sink.PublishedControlCount())
	a.Mu.Lock()
	registered := a.root.controls[0]
	a.Mu.Unlock()

	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{
		waitingStep(`{"permission":{"actionDescription":"Run echo","resource":{"target":"echo marker","action":"command"}}}`, 0),
	}))
	assert.Equal(t, 1, sink.PublishedControlCount(), "the same interaction publishes no second request")
	assert.Empty(t, sink.CanceledControls(), "the same interaction keeps its card")
	a.Mu.Lock()
	assert.Same(t, registered, a.root.controls[0])
	a.Mu.Unlock()
}

// A step can replace a published interaction with one that LeapMux cannot
// answer. The published request then answers nothing that the language
// server waits for, so the Worker withdraws it when it reports the fault and
// cancels the cascade (refuseInteraction).
func TestAntigravityControlPublicationWithdrawsARequestThatAnUnanswerableInteractionReplaces(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	require.Equal(t, 1, sink.PublishedControlCount())
	request := sink.LastPublishedControl()

	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingStep(`{"browserAction":{}}`, 0)}))
	assert.Equal(t, []string{request.RequestID}, sink.CanceledControls(), "the replaced request is withdrawn")
	sink.mu.Lock()
	assert.Empty(t, sink.active, "no card stays for the step")
	sink.mu.Unlock()
	assert.Equal(t, 1, sink.PublishedControlCount(), "the unanswerable interaction is no request")
	assert.Equal(t, 1, server.cancelCount(), "the cascade stops waiting for an answer that never comes")
	require.Len(t, sink.Notifications(), 1)
	assert.Contains(t, sink.Notifications()[0][contracts.NotificationFieldText], "browserAction")
	a.Mu.Lock()
	assert.Empty(t, a.root.controls)
	a.Mu.Unlock()
}

// LeapMux reads an interaction's fields leniently, as ReadControlRequest
// does. A permission whose resource the approval policy cannot read is the
// user's to answer, and a question list that holds an element that is no
// question is still a question. Neither leaves the step waiting with no
// request.
func TestAntigravityControlPublicationPublishesAnInteractionItReadsInPart(t *testing.T) {
	for name, tc := range map[string]struct {
		interaction string
		question    bool
	}{
		"a permission with no readable resource": {interaction: `{"permission":{"resource":"echo marker"}}`},
		"a question list with a non-question":    {interaction: `{"askQuestion":{"questions":[{"question":"Continue?","options":[]},7]}}`, question: true},
	} {
		t.Run(name, func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			rpc, server := newRecordedLanguageServer(t)
			a.Mu.Lock()
			a.rpc = rpc
			a.mode = contracts.AntigravityModeBypass
			a.Mu.Unlock()
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingStep(tc.interaction, 0)}))
			waitForApprovalWork(t, a)
			assert.Empty(t, server.answered(), "the policy approves only a permission that it reads")
			require.Equal(t, 1, sink.PublishedControlCount())
			assert.Empty(t, sink.Notifications())
			prompt := agenttest.ControlPromptOf(antigravityProvider{}, sink.LastPublishedControl().Payload)
			if tc.question {
				require.NotNil(t, prompt.GetQuestion())
				assert.Len(t, prompt.GetQuestion().GetQuestions(), 1)
			} else {
				assert.NotNil(t, prompt.GetPermission())
			}
		})
	}
}

func TestAntigravityControlPublicationRetriesExistingInteraction(t *testing.T) {
	for _, kind := range []string{"permission", "question"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			sink.fail.Store(true)
			update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep(kind)})
			feedUpdate(t, a, update)
			require.Zero(t, sink.PublishedControlCount())
			a.Mu.Lock()
			instance := a.root.controls[0].instanceID
			a.Mu.Unlock()
			sink.fail.Store(false)
			feedUpdate(t, a, update)
			request := receiveTestValue(t, sink.stored)
			assert.Equal(t, instance, request.InstanceID)
			feedUpdate(t, a, update)
			assert.Equal(t, 1, sink.PublishedControlCount())
		})
	}
}

// A step whose interaction LeapMux cannot answer is no control request: the
// Worker reports the fault and cancels the cascade instead
// (refuseInteraction). The language server can replace the interaction of a
// registered step while its automatic approval is in flight. The replacement
// withdraws the step's control, so the failed approval publishes no request,
// because no answer to it would reach the language server.
func TestAntigravityControlPublicationSkipsAStepWithNoInteraction(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	a.Mu.Lock()
	a.mode = contracts.AntigravityModeBypass
	a.Mu.Unlock()
	entered := make(chan struct{}, 1)
	release, unblock := newTestGate(t)
	var cancels atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, languageServerService) {
		case "CancelCascadeInvocation":
			cancels.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case "HandleCascadeUserInteraction":
			entered <- struct{}{}
			<-release
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("approval refused"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	receiveTestValue(t, entered)

	dropped, err := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusWaiting,
		"metadata": map[string]any{"toolCall": map[string]any{"id": "waiting-call", "name": "run_command"}},
	})
	require.NoError(t, err)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{dropped}))
	assert.Equal(t, int32(1), cancels.Load(), "the reader cancels a cascade whose interaction it cannot answer")
	require.Len(t, sink.Notifications(), 1)
	assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0][contracts.NotificationFieldType])

	unblock()
	waitForApprovalWork(t, a)
	assert.Zero(t, sink.PublishedControlCount(), "a step with no interaction is no control request")
}

// The provider reads no later frame of a step that it consumed
// (applyUpdate), so no later frame can withdraw that step's request. A frame
// that LeapMux cannot decode consumes the step as an unknown step, so the
// step's request ends with it. An automatic approval that fails after that
// publishes nothing.
func TestAntigravityControlPublicationEndsWithAnUndecodableStep(t *testing.T) {
	undecodable := []json.RawMessage{json.RawMessage(`{"status":"CORTEX_STEP_STATUS_WAITING"}`)}
	t.Run("a published request", func(t *testing.T) {
		a, sink := publicationTestAgent(t, nil)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
		require.Equal(t, 1, sink.PublishedControlCount())
		request := sink.LastPublishedControl()
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, undecodable))
		assert.Equal(t, []string{request.RequestID}, sink.CanceledControls())
		sink.mu.Lock()
		assert.Empty(t, sink.active)
		sink.mu.Unlock()
		a.Mu.Lock()
		assert.True(t, a.root.steps[0].consumed)
		assert.Empty(t, a.root.controls)
		a.Mu.Unlock()
	})
	t.Run("a claimed approval", func(t *testing.T) {
		a, sink := publicationTestAgent(t, nil)
		entered := make(chan struct{}, 1)
		release, unblock := newTestGate(t)
		a.Mu.Lock()
		a.mode = contracts.AntigravityModeBypass
		a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("approval refused"))
		})
		a.Mu.Unlock()
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
		receiveTestValue(t, entered)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, undecodable))
		unblock()
		waitForApprovalWork(t, a)
		assert.Zero(t, sink.PublishedControlCount(), "a consumed step has no request")
		a.Mu.Lock()
		assert.Empty(t, a.root.controls)
		a.Mu.Unlock()
	})
}

// An idle frame ends the turn, and the end of the turn persists each step
// that is still open as it stands and consumes it (flushOpenSteps). A step
// that still waits then has no request either, because the provider reads no
// later frame of it.
func TestAntigravityControlPublicationEndsWithTheTurn(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	require.Equal(t, 1, sink.PublishedControlCount())
	request := sink.LastPublishedControl()
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	assert.Equal(t, []string{request.RequestID}, sink.CanceledControls())
	sink.mu.Lock()
	assert.Empty(t, sink.active)
	sink.mu.Unlock()
	a.Mu.Lock()
	assert.True(t, a.root.steps[0].consumed)
	assert.Empty(t, a.root.controls)
	assert.Equal(t, cascadeTurnIdle, a.root.turnPhase)
	a.Mu.Unlock()
}

func TestAntigravityControlPublicationCannotOutliveRetirement(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "withdraw", true: "retire all"}[bulk], func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			sink.entered = make(chan struct{}, 1)
			var unblock func()
			sink.release, unblock = newTestGate(t)
			done := make(chan struct{})
			go func() {
				_ = a.applyUpdate(a.root, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}), false)
				close(done)
			}()
			receiveTestValue(t, sink.entered)
			if bulk {
				a.retireAllInteractions(a.root)
			} else {
				a.withdrawInteraction(a.root, 0)
			}
			unblock()
			receiveTestValue(t, done)
			a.Mu.Lock()
			assert.Empty(t, a.root.controls)
			a.Mu.Unlock()
			sink.mu.Lock()
			assert.Empty(t, sink.active)
			sink.mu.Unlock()
			assert.Len(t, sink.CanceledControls(), 1)
		})
	}
}

func TestAntigravityControlPublicationRecoversWithoutAnotherFrame(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink := publicationTestAgent(t, clock)
	queue := sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	sink.fail.Store(true)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	require.True(t, queue.HasPending(), "publication must retain its retry without another native frame")
	sink.fail.Store(false)
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	request := receiveTestValue(t, sink.stored)
	queue.StopRetries()
	assert.Equal(t, "antigravity:cascade:trajectory:0", request.RequestID)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.False(t, queue.HasPending())
}

// A plan review outlives its turn, so the user can answer it later. Each
// review has a key of its own, and the next plan of the cascade supersedes
// it: the Worker withdraws the earlier card, and the user answers the latest
// plan alone.
func TestAntigravityPlanReviewSupersedesTheEarlierReview(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	a.mode = contracts.AntigravityModePlan
	a.homeDir = t.TempDir()
	path := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", a.root.id, "implementation_plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	review := func(plan string) agent.ControlRequest {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(plan), 0o600))
		a.beginTurn(a.root)
		a.root.planPath = path
		require.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
		return receiveTestValue(t, sink.stored)
	}
	first := review("# First plan\n")
	assert.Empty(t, sink.CanceledControls(), "the end of its turn leaves the review open")
	second := review("# Second plan\n")

	assert.NotEqual(t, first.RequestID, second.RequestID, "each review has a key of its own")
	assert.Equal(t, []string{first.RequestID}, sink.CanceledControls(), "the later plan withdraws the earlier review")
	sink.mu.Lock()
	assert.Equal(t, map[string]string{second.RequestID: second.InstanceID}, sink.active)
	sink.mu.Unlock()
	assert.Contains(t, string(second.Payload), "Second plan")
	agenttest.AssertPublishedControlKeys(t, antigravityProvider{}, sink.ControlSink)
}

func TestAntigravityPlanPublicationRecoversWithoutAnotherTurn(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, sink := publicationTestAgent(t, clock)
	queue := sink.OutputQueue()
	queue.ConfigureRetry(t.Context(), clock, nil)
	t.Cleanup(queue.StopRetries)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
	a.mode = contracts.AntigravityModePlan
	a.homeDir = t.TempDir()
	path := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", a.root.id, "implementation_plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("# Retained plan\n\nVerify this plan.\n"), 0o600))
	a.beginTurn(a.root)
	a.root.planPath = path
	sink.fail.Store(true)
	require.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
	require.True(t, queue.HasPending(), "a failed plan publication must remain available for retry")
	sink.fail.Store(false)
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	request := receiveTestValue(t, sink.stored)
	queue.StopRetries()
	assert.Contains(t, string(request.Payload), contracts.AntigravityControlToolPlanReview)
	assert.Contains(t, string(request.Payload), "Verify this plan.")
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, 1, sink.PlanUpdateCount())
	assert.False(t, queue.HasPending())
}
