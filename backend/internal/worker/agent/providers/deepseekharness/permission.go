package deepseekharness

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The native settings command applies to the root session. Child sessions keep their own policy.
func (a *Agent) canBypassControlLocked(control *nativeControl) bool {
	return !a.StoppedLocked() && a.streamFailure == nil && control.agentID == a.sessionID &&
		a.mode == contracts.DeepseekHarnessModeAct && a.permissions == contracts.DeepseekHarnessPermissionPresetDangerFullAccess &&
		control.event == contracts.DeepseekHarnessControlEventApproval && control.toolName != "" &&
		control.toolName != contracts.DeepseekHarnessToolAskUserQuestion && control.toolName != contracts.DeepseekHarnessToolExitPlanMode && !control.automaticFailed
}

func (a *Agent) queuePermissionApprovals() {
	a.Mu.Lock()
	needed := false
	for _, control := range a.controls {
		if control.ready && !control.answering && (a.canBypassControlLocked(control) || !control.published) {
			needed = true
			break
		}
	}
	a.Mu.Unlock()
	if !needed {
		return
	}
	a.approvalWork.Run("permissions", func() {
		a.opMu.Lock()
		defer a.opMu.Unlock()
		a.approvePendingPermissions()
	})
}

func (a *Agent) approvePendingPermissions() {
	a.Mu.Lock()
	claimed := make(map[string]*nativeControl)
	publish := make(map[string]*nativeControl)
	for id, control := range a.controls {
		if !control.ready || control.answering {
			continue
		}
		if a.canBypassControlLocked(control) {
			control.answering = true
			claimed[id] = control
		} else if !control.published {
			control.ready = false
			publish[id] = control
		}
	}
	a.Mu.Unlock()
	for id, control := range publish {
		if err := a.publishRegisteredControl(id, control); err != nil {
			a.reportStreamFailure(err)
			return
		}
	}
	for id, control := range claimed {
		a.Mu.Lock()
		current := a.controls[id] == control && !a.StoppedLocked() && a.streamFailure == nil
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := a.deliverControlOutcome(id, control, map[string]any{"kind": "result", "value": "allowed-once"}, true)
		if err != nil {
			slog.Warn("DeepSeek Harness could not approve a pending tool", "request_id", id, "error", err)
		}
	}
}

func (a *Agent) deliverControlOutcome(id string, control *nativeControl, outcome map[string]any, automatic bool) error {
	err := a.replyRemoteEvent(id, outcome)
	if err != nil {
		var native *remoteFailure
		if !errors.As(err, &native) {
			err = providerkit.ClassifyHTTPDeliveryError("DeepSeek Harness control answer", err)
		}
	}
	a.Mu.Lock()
	current := a.controls[id] == control
	published := control.published
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	publish := false
	if current {
		if err == nil {
			delete(a.controls, id)
		} else if uncertain {
			control.published = false
		} else {
			control.answering = false
			control.automaticFailed = control.automaticFailed || automatic
			publish = !control.published
			if publish {
				control.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if current && published && (automatic && err == nil || uncertain) {
		control.sink.CancelControlRequest(id)
	}
	if current && publish {
		if publicationErr := a.publishRegisteredControl(id, control); publicationErr != nil {
			a.reportStreamFailure(publicationErr)
		}
	}
	return err
}

func (a *Agent) sendControlOutcome(id string, control *nativeControl, outcome map[string]any) error {
	a.Mu.Lock()
	if a.controls[id] != control || !control.published || control.answering || a.StoppedLocked() || a.streamFailure != nil {
		a.Mu.Unlock()
		return fmt.Errorf("DeepSeek Harness control response has no pending request")
	}
	control.answering = true
	a.Mu.Unlock()
	return a.deliverControlOutcome(id, control, outcome, false)
}
