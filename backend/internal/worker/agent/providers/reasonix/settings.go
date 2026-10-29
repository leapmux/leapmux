package reasonix

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// setReasonixMode makes settings changes invalidate the same goal confirmations as goal commands.
func (a *Agent) setReasonixMode(mode string, acknowledged func(string)) error {
	return a.WithSessionID(func(sessionID string) error {
		return a.setReasonixSessionMode(sessionID, mode, acknowledged)
	})
}

// The caller owns the session lock. Goal preparation already holds its exclusive form.
func (a *Agent) setReasonixSessionMode(sessionID, mode string, acknowledged func(string)) error {
	if sessionID == "" || a.IsStopped() {
		return fmt.Errorf("the Reasonix session is unavailable")
	}
	if acknowledged == nil {
		return fmt.Errorf("the Reasonix mode writer requires an acknowledgment callback")
	}
	if modes := a.AvailableModes(); len(modes) > 0 && !acp.HasOption(modes, mode) {
		return fmt.Errorf("unknown Reasonix mode: %s", mode)
	}
	params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modeId": mode})
	if err != nil {
		return err
	}
	var objectErr error
	_, err = a.SendRequestObserved(acp.MethodSessionSetMode, params, a.APITimeout(), func(raw json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			objectErr = fmt.Errorf("the Reasonix mode reply must be an object")
			return
		}
		acknowledged(mode)
		if mode == contracts.ReasonixModeNormal || mode == contracts.ReasonixModePlan {
			a.acknowledgeReasonixGoalClear()
		}
	})
	if err != nil {
		return err
	}
	return objectErr
}
