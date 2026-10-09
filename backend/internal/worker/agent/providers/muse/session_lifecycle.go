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
		params["excludeItems"] = true
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
	_, err = a.request(method, payload, timeout, func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var response sessionResult
		if decode := json.Unmarshal(raw, &response); decode != nil {
			decodedErr = decode
			return
		}
		if response.Session.ID == "" {
			decodedErr = fmt.Errorf("the Muse host returned an invalid session")
			return
		}
		a.stateMu.Lock()
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
			a.sessions[a.sessionID] = &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog(a.sessionID)}
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
	return a.subscribeLog(a.sessionID, timeout)
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
	// Commit the replacement, then retire the old session state BEFORE its
	// subscriptions close: every late native frame of the old session then
	// meets a retired state and changes no live turn or transcript.
	a.stateMu.Lock()
	replacement := a.sessionID
	if previous := a.sessions[old]; previous != nil && old != replacement {
		previous.retired = true
		previous.turnID = ""
	}
	logSubscription := int64(0)
	if previous := a.sessions[old]; previous != nil {
		logSubscription = previous.log.subscriptionID
	}
	a.stateMu.Unlock()
	a.sink.ResetSpans()
	a.sink.UpdateSessionID(replacement)
	// Both native subscriptions of the old session close after the commit. A
	// refusal there cannot roll the committed replacement back, so it is
	// logged and swallowed: the host stops delivering either way, and the
	// retired state already ignores whatever still arrives.
	if _, err := a.request(methodViewUnsubscribe, map[string]string{"sessionId": old}, a.APITimeout(), nil); err != nil {
		slog.Warn("muse view unsubscribe after context clear failed", "session", old, "error", err)
	}
	if logSubscription != 0 {
		if _, err := a.request(methodLogUnsubscribe, map[string]any{"subscriptionId": logSubscription}, a.APITimeout(), nil); err != nil {
			slog.Warn("muse log unsubscribe after context clear failed", "session", old, "error", err)
		}
	}
	return replacement, nil
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
	_, err := a.command(methodSessionCompact, map[string]any{"sessionId": id}, a.APITimeout(), nil)
	// The installed host refuses a manual compaction it cannot run
	// (`compaction_unavailable`), which states the same fact as an agent that
	// offers no compaction: the command degrades to an ordinary message.
	var responseError *providerkit.JSONRPCResponseError
	if err != nil && errors.As(err, &responseError) && strings.Contains(string(responseError.Data), "compaction_unavailable") {
		return agent.ErrCompactionUnsupported
	}
	return err
}
