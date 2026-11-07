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
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/optionmap"
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
	var step antigravityStep
	require.NoError(t, json.Unmarshal(raw, &step))
	var interaction requestedInteraction
	require.NoError(t, json.Unmarshal(step.RequestedInteraction, &interaction))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	control := &antigravityControl{
		requestID:  fmt.Sprintf("antigravity:%s:%s:%d", a.root.id, a.root.trajectoryID, index),
		instanceID: id.Generate(), cascadeID: a.root.id, trajectoryID: a.root.trajectoryID,
		stepIndex: index, kind: "permission", permission: interaction.Permission,
	}
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
