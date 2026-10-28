package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) openSession(resume string, timeout time.Duration) error {
	params := map[string]any{}
	method := methodSessionStart
	if resume != "" {
		id, err := (museProvider{}).ResolveResumeHandle(resume, a.opts.HomeDir)
		if err != nil {
			return err
		}
		params["sessionId"] = id
		params["excludeItems"] = false
		params["history"] = "inline"
		method = methodSessionResume
	} else {
		params["workspaceRoot"] = a.opts.WorkingDir
		if !agent.UsesAccountDefaultModel(a.opts.Model()) {
			params["modelId"] = a.opts.Model()
		}
		if a.opts.PermissionMode() != "" {
			params["approvalMode"] = a.opts.PermissionMode()
		}
	}
	payload, _, err := commandParams(params)
	if err != nil {
		return err
	}
	var decodedErr error
	var history json.RawMessage
	var head string
	_, err = a.request(method, payload, timeout, func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var response sessionResult
		if decode := json.Unmarshal(raw, &response); decode != nil {
			decodedErr = decode
			return
		}
		if resume != "" {
			var identity struct {
				ViewCursor *string `json:"viewCursor"`
			}
			if json.Unmarshal(raw, &identity) != nil || identity.ViewCursor == nil {
				decodedErr = fmt.Errorf("the Muse host returned an invalid native history cursor")
				return
			}
		}
		if response.Session.ID == "" || resume != "" && response.Session.ID != resume {
			decodedErr = fmt.Errorf("the Muse host returned an invalid session")
			return
		}
		history, head = response.History, response.ViewCursor
		a.stateMu.Lock()
		if a.sessionID != response.Session.ID {
			a.retireSessionTreeLocked(a.sessionID)
		}
		a.sessionID = response.Session.ID
		a.settings = make(optionmap.Map)
		if response.Session.ModelID != nil && *response.Session.ModelID != "" {
			a.settings[agent.OptionIDModel] = *response.Session.ModelID
		}
		for _, descriptor := range contracts.MuseStartupOptionGroups {
			value := a.opts.Get(descriptor.ID)
			if value == "" {
				value = descriptor.DefaultValue
			}
			a.settings[descriptor.ID] = value
		}
		if response.Session.ApprovalMode != nil {
			a.settings[agent.OptionIDPermissionMode] = response.Session.ApprovalMode.Mode
		}
		if a.sessions[a.sessionID] == nil {
			a.sessions[a.sessionID] = &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog(a.sessionID)}
		}
		state := a.sessions[a.sessionID]
		if response.Session.ActiveTurnID != nil {
			state.turnID = *response.Session.ActiveTurnID
		}
		a.stateMu.Unlock()
	})
	if err != nil {
		return err
	}
	if decodedErr != nil {
		return decodedErr
	}
	if resume != "" {
		if err := a.restoreChildRoutes(a.sessionID, history, head); err != nil {
			return fmt.Errorf("restore the Muse child conversations: %w", err)
		}
	}
	return a.subscribeLog(a.sessionID, timeout)
}

// retireSessionTreeLocked marks the whole outgoing tree in the same critical
// section as root replacement. The caller holds stateMu. Received transcript
// evidence remains available, while no descendant can publish live state.
func (a *Agent) retireSessionTreeLocked(root string) map[string]*sessionState {
	states := make(map[string]*sessionState)
	if root == "" || a.sessions[root] == nil {
		return states
	}
	children := make(map[string][]string)
	for id, state := range a.sessions {
		if state.parentSessionID != "" {
			children[state.parentSessionID] = append(children[state.parentSessionID], id)
		}
	}
	pending := []string{root}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if states[id] != nil {
			continue
		}
		state := a.sessions[id]
		state.retired = true
		if state.projection != nil {
			state.projection.signal()
		}
		states[id] = state
		pending = append(pending, children[id]...)
	}
	return states
}

func (a *Agent) ClearContext() (string, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.stateMu.Lock()
	old := a.sessionID
	state := a.sessions[old]
	busy := state != nil && state.turnID != ""
	a.stateMu.Unlock()
	if busy {
		return "", agent.ErrAgentBusy
	}
	if err := a.openSession("", a.APITimeout()); err != nil {
		return "", err
	}
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	replacement := a.sessionID
	var retired map[string]*sessionState
	if old != replacement {
		retired = a.retireSessionTreeLocked(old)
	}
	a.stateMu.Unlock()
	a.retireSessionStates(retired, agent.MessageCompletionInterrupted)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
	a.sink.ResetSpans()
	a.sink.UpdateSessionID(replacement)
	if old != replacement {
		a.sink.ClearGoal(false)
	}
	// Close both old subscriptions after the session replacement.
	// A refusal cannot undo the replacement. Log the refusal and keep the replacement.
	// The retired state prevents later frames from changing the replacement's live state.
	for id, previous := range retired {
		a.unsubscribeRetiredSession(id, previous)
	}
	return replacement, nil
}

// unsubscribeRetiredSession waits for every earlier admitted registration.
// The caller has already retired the state and holds no dispatcher or state lock.
func (a *Agent) unsubscribeRetiredSession(id string, previous *sessionState) {
	release, err := a.acquireSessionSubscription(previous)
	if err != nil {
		slog.Debug("the Muse process ended before subscription cleanup", "session", id, "error", err)
		return
	}
	defer release()
	if _, err := a.request(methodViewUnsubscribe, map[string]string{"sessionId": id}, a.APITimeout(), nil); err != nil {
		slog.Warn("muse view unsubscribe after context clear failed", "session", id, "error", err)
	}
	a.stateMu.Lock()
	logSubscription := int64(0)
	if previous.log != nil {
		logSubscription = previous.log.subscriptionID
	}
	a.stateMu.Unlock()
	if logSubscription != 0 {
		var observedErr error
		_, err := a.request(methodLogUnsubscribe, map[string]any{"subscriptionId": logSubscription}, a.APITimeout(), func(raw json.RawMessage, nativeErr error) {
			if nativeErr != nil {
				return
			}
			var acknowledgement map[string]json.RawMessage
			if json.Unmarshal(raw, &acknowledgement) != nil || acknowledgement == nil {
				observedErr = fmt.Errorf("the Muse log unsubscribe acknowledgement is invalid")
				return
			}
			a.stateMu.Lock()
			if previous.log != nil && previous.log.subscriptionID == logSubscription {
				previous.log.subscriptionID = 0
			}
			a.stateMu.Unlock()
		})
		if err = errors.Join(err, observedErr); err != nil {
			slog.Warn("muse log unsubscribe after context clear failed", "session", id, "error", err)
		}
	}
}

func (a *Agent) CompactContext() error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.stateMu.Lock()
	id := a.sessionID
	state := a.sessions[id]
	busy := state != nil && state.turnID != ""
	a.stateMu.Unlock()
	if busy {
		return agent.ErrAgentBusy
	}
	raw, err := a.command(methodSessionCompact, map[string]any{"sessionId": id}, a.APITimeout(), nil)
	if err == nil {
		// A noop compaction changes no context and emits no item.
		// Its acknowledgement ends the worker's compaction turn.
		var reply struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &reply) == nil && reply.Status == contracts.MuseCompactionOutcomeNoop {
			a.PublishTurnActive()
		}
	}
	// The native host returns compaction_unavailable for a manual compaction that it cannot run.
	// Report unsupported compaction so the worker sends the command as ordinary model input.
	var responseError *providerkit.JSONRPCResponseError
	if err != nil && errors.As(err, &responseError) && strings.Contains(string(responseError.Data), "compaction_unavailable") {
		return agent.ErrCompactionUnsupported
	}
	return err
}
