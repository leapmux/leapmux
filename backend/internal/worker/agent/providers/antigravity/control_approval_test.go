package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedApprovalControl(t *testing.T, a *Agent, index int, action, target string) *antigravityControl {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusWaiting,
		"metadata": map[string]any{"toolCall": map[string]any{"id": fmt.Sprintf("call-%d", index), "name": "run_command"}},
		"requestedInteraction": map[string]any{"permission": map[string]any{
			"resource": map[string]any{"action": action, "target": target}, "actionDescription": "Approve " + target,
		}},
	})
	require.NoError(t, err)
	step, err := decodeStep(raw)
	require.NoError(t, err)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	control, err := newInteractionControl(a.root.id, a.root.trajectoryID, index, step)
	require.NoError(t, err)
	a.root.controls[index] = control
	a.root.steps[index] = &stepState{status: contracts.AntigravityStepStatusWaiting, raw: raw}
	return control
}

func waitForApprovalWork(t *testing.T, a *Agent) {
	t.Helper()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, 5*time.Millisecond)
}

func approvedNotificationCount(sink *interactionPublicationSink) int {
	count := 0
	for _, notification := range sink.Notifications() {
		if notification[contracts.NotificationFieldType] == contracts.NotificationTypePendingRequestsApproved {
			count++
		}
	}
	return count
}

func TestAntigravityAutomaticApprovalUncertaintyPublishesAndContinues(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	seedApprovalControl(t, a, 0, contracts.AntigravityPermissionActionCommand, "first command")
	seedApprovalControl(t, a, 1, contracts.AntigravityPermissionActionCommand, "second command")
	var requests atomic.Int32
	var failedIndex atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Interaction struct {
				StepIndex int `json:"stepIndex"`
			} `json:"interaction"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if requests.Add(1) == 1 {
			failedIndex.Store(int32(request.Interaction.StepIndex))
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeBypass})
	waitForApprovalWork(t, a)
	assert.Equal(t, int32(2), requests.Load())
	require.Equal(t, 1, sink.PublishedControlCount())
	var published struct {
		StepIndex int `json:"stepIndex"`
	}
	require.NoError(t, json.Unmarshal(sink.LastPublishedControl().Payload, &published))
	assert.Equal(t, int(failedIndex.Load()), published.StepIndex)
	assert.Equal(t, 1, approvedNotificationCount(sink))
	a.queuePermissionApprovals()
	waitForApprovalWork(t, a)
	assert.Equal(t, int32(2), requests.Load(), "uncertain approval must not be sent automatically again")
	assert.Equal(t, 1, sink.PublishedControlCount())
}

func TestAntigravityAutomaticApprovalChecksRefusalFreshness(t *testing.T) {
	for _, mutation := range []string{"pending", "retired", "replaced"} {
		t.Run(mutation, func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			original := seedApprovalControl(t, a, 0, contracts.AntigravityPermissionActionCommand, "original command")
			entered := make(chan struct{}, 1)
			release, unblock := newTestGate(t)
			var requests atomic.Int32
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				entered <- struct{}{}
				<-release
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("input not registered"))
			})
			a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeBypass})
			receiveTestValue(t, entered)
			var replacement *antigravityControl
			if mutation != "pending" {
				a.withdrawInteraction(a.root, 0)
				if mutation == "replaced" {
					replacement = seedApprovalControl(t, a, 0, "unknown_action", "replacement request")
				}
			}
			unblock()
			waitForApprovalWork(t, a)
			assert.Equal(t, int32(1), requests.Load())
			assert.Zero(t, approvedNotificationCount(sink))
			switch mutation {
			case "pending":
				require.Equal(t, 1, sink.PublishedControlCount())
				assert.Equal(t, original.instanceID, sink.LastPublishedControl().InstanceID)
			case "replaced":
				require.Equal(t, 1, sink.PublishedControlCount())
				assert.Equal(t, replacement.instanceID, sink.LastPublishedControl().InstanceID)
			default:
				assert.Zero(t, sink.PublishedControlCount())
			}
		})
	}
}

func TestAntigravityPermissionModeChangesReuseApprovalPolicy(t *testing.T) {
	for _, scenario := range []string{"unknown bypass", "eligible edit", "outside edit"} {
		t.Run(scenario, func(t *testing.T) {
			a, sink := publicationTestAgent(t, nil)
			action, target, mode := "unknown_action", "unknown resource", contracts.AntigravityModeBypass
			if scenario != "unknown bypass" {
				action, mode = contracts.AntigravityPermissionActionWriteFile, contracts.AntigravityModeAcceptEdits
				target = filepath.Join(a.workspace, "new.txt")
				if scenario == "outside edit" {
					target = filepath.Join(t.TempDir(), "outside.txt")
				}
			}
			seedApprovalControl(t, a, 0, action, target)
			var requests atomic.Int32
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{}`))
			})
			a.UpdateSettings(optionmap.Map{"permissionMode": mode})
			waitForApprovalWork(t, a)
			if scenario == "eligible edit" {
				assert.Equal(t, int32(1), requests.Load())
				assert.Zero(t, sink.PublishedControlCount())
			} else {
				assert.Zero(t, requests.Load())
				assert.Equal(t, 1, sink.PublishedControlCount())
			}
		})
	}
}

// The automatic approval policy reads the interaction that the step requests
// now. A permission that the step replaced with a question answers nothing,
// so a switch to Bypass sends no permission answer to the question.
func TestAntigravityPermissionModeChangeReadsTheCurrentInteraction(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeBypass})
	waitForApprovalWork(t, a)
	assert.Empty(t, server.answered(), "no permission answer reaches a step that asks a question")
	require.Equal(t, 2, sink.PublishedControlCount())
	assert.NotNil(t, agenttest.ControlPromptOf(antigravityProvider{}, sink.LastPublishedControl().Payload).GetQuestion())
}

// An automatic approval claims the interaction that it answers, not the
// step. The language server keeps each answered interaction of a step in
// `completedInteractions`, and the step can then request another one. The
// policy answers that one too, even when it repeats the first, and it does
// not answer a repeated frame of an interaction that it answered.
func TestAntigravityAutomaticApprovalAnswersEachInteractionOfAStep(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.mode = contracts.AntigravityModeBypass
	a.Mu.Unlock()
	first := waitingStep(`{"permission":{"resource":{"action":"command","target":"echo first"}}}`, 0)
	second := `{"permission":{"resource":{"action":"command","target":"echo second"}}}`
	for _, tc := range []struct {
		step    json.RawMessage
		answers int
	}{
		{step: first, answers: 1},
		{step: first, answers: 1},
		{step: waitingStep(second, 1), answers: 2},
		{step: waitingStep(second, 2), answers: 3},
	} {
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{tc.step}))
		waitForApprovalWork(t, a)
		require.Len(t, server.answered(), tc.answers)
	}
	for _, answer := range server.answered() {
		assert.JSONEq(t, `{"trajectoryId":"trajectory","stepIndex":0,"permission":{"allow":true,"scope":"PERMISSION_SCOPE_ONCE"}}`, string(answer))
	}
	assert.Zero(t, sink.PublishedControlCount())
}

// An automatic approval answers the control instance that the policy read,
// and the language server addresses the answer to the step. The approval
// publishes the requests that the policy leaves to the user after it claims
// its own, so the step can replace the approved permission before the
// approval reaches the language server. The approval then reaches nothing,
// because it would answer the step's new interaction, and the user sees the
// new request.
//
// The test holds the approval in that window. A request whose earlier
// publication failed waits in the output queue, so the approval publishes
// it, and the sink holds that publication.
func TestAntigravityAutomaticApprovalDropsAReplacedInteraction(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.Mu.Unlock()
	sink.fail.Store(true)
	approvable := waitingStep(`{"permission":{"resource":{"action":"command","target":"echo approved"}}}`, 0)
	manual := waitingStep(`{"permission":{"resource":{"action":"unknown_action","target":"manual"}}}`, 0)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{approvable, manual}))
	require.Zero(t, sink.PublishedControlCount(), "both publications wait in the output queue")
	a.Mu.Lock()
	approved := a.root.controls[0]
	a.Mu.Unlock()
	require.NotNil(t, approved)
	sink.fail.Store(false)
	sink.entered = make(chan struct{}, 8)
	var unblock func()
	sink.release, unblock = newTestGate(t)

	a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeBypass})
	receiveTestValue(t, sink.entered)
	a.Mu.Lock()
	claimed := approved.approvalClaimed
	a.Mu.Unlock()
	require.True(t, claimed, "the approval claimed the permission before it publishes the manual request")
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	unblock()
	waitForApprovalWork(t, a)

	assert.Empty(t, server.answered(), "the approval of the replaced permission reaches no interaction")
	assert.Zero(t, approvedNotificationCount(sink))
	a.Mu.Lock()
	current := a.root.controls[0]
	a.Mu.Unlock()
	require.NotNil(t, current)
	assert.NotEqual(t, approved.instanceID, current.instanceID)
	var published []string
	for _, request := range sink.PublishedControls() {
		published = append(published, request.InstanceID)
	}
	assert.Contains(t, published, current.instanceID, "the user sees the question that replaced the permission")
}

func TestAntigravityPermissionModeRevokesUnclaimedApprovals(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	seedApprovalControl(t, a, 0, contracts.AntigravityPermissionActionCommand, "first command")
	seedApprovalControl(t, a, 1, contracts.AntigravityPermissionActionCommand, "second command")
	entered := make(chan int, 2)
	release, unblock := newTestGate(t)
	var requests atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Interaction struct {
				StepIndex int `json:"stepIndex"`
			} `json:"interaction"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if requests.Add(1) == 1 {
			entered <- request.Interaction.StepIndex
			<-release
		}
		_, _ = w.Write([]byte(`{}`))
	})
	a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeBypass})
	claimed := receiveTestValue(t, entered)
	a.UpdateSettings(optionmap.Map{"permissionMode": contracts.AntigravityModeDefault})
	unblock()
	waitForApprovalWork(t, a)
	assert.Equal(t, int32(1), requests.Load())
	require.Equal(t, 1, sink.PublishedControlCount())
	var manual struct {
		StepIndex int `json:"stepIndex"`
	}
	require.NoError(t, json.Unmarshal(sink.LastPublishedControl().Payload, &manual))
	assert.NotEqual(t, claimed, manual.StepIndex)
	a.queuePermissionApprovals()
	waitForApprovalWork(t, a)
	assert.Equal(t, int32(1), requests.Load())
	assert.Equal(t, 1, sink.PublishedControlCount())
}
