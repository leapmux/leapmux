package mimo

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// MiMo's permission switches apply to every actor in the owned session.
// Questions and plan reviews use separate native routes.
func (a *Agent) canBypassControlLocked(control *mimoControl) bool {
	return control.kind == controlPermission && !control.automaticFailed &&
		a.permissionPolicy == contracts.MiMoPermissionPolicyBypass && a.ownsSessionLocked(control.sessionID) && !a.sessionSwitching && !a.StoppedLocked()
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
	if needed {
		a.approvalWork.Run("permissions", a.approvePendingPermissions)
	}
}

func (a *Agent) approvePendingPermissions() {
	a.Mu.Lock()
	claimed := make(map[string]*mimoControl)
	publish := make(map[string]*mimoControl)
	for id, control := range a.controls {
		if !control.ready || control.answering {
			continue
		}
		if a.canBypassControlLocked(control) {
			control.answering = true
			control.automatic = true
			claimed[id] = control
		} else if !control.published {
			control.ready = false
			publish[id] = control
		}
	}
	a.Mu.Unlock()
	for id, control := range publish {
		a.publishRegisteredControl(id, control)
	}
	for id, control := range claimed {
		a.Mu.Lock()
		current := a.controls[id] == control && !a.StoppedLocked() && !a.sessionSwitching && a.ownsSessionLocked(control.sessionID)
		a.Mu.Unlock()
		if !current {
			continue
		}
		err := providerkit.ClassifyHTTPDeliveryError("MiMo automatic approval", a.rpc.replyPermission(a.Context(), control.nativeID, mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyOnce}))
		a.finishControlAnswer(id, control, err)
		if err != nil {
			slog.Warn("MiMo could not approve a pending tool", "request_id", id, "error", err)
		}
	}
}

func (a *Agent) finishControlAnswer(id string, control *mimoControl, err error) {
	a.Mu.Lock()
	current := a.controls[id] == control
	published := control.published
	automatic := control.automatic
	uncertain := errors.Is(err, agent.ErrDeliveryUncertain)
	gone := providerkit.IsHTTPStatus(err, http.StatusNotFound)
	publish := false
	if current {
		if gone {
			delete(a.controls, id)
		} else if err == nil || uncertain {
			// Keep the claim until settlement. A reconnect can replay an older pending-request snapshot.
			control.answered = true
			control.published = false
		} else {
			control.answering = false
			control.automaticFailed = control.automaticFailed || automatic
			publish = !control.published && control.ready
			if publish {
				control.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if current && published && (err == nil && automatic || uncertain || gone) {
		a.sink.CancelControlRequestInstance(id, control.instanceID)
	}
	if current && publish {
		a.publishRegisteredControl(id, control)
	}
}
