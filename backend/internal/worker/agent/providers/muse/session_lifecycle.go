package muse

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	a.sink.ResetSpans()
	a.sink.UpdateSessionID(a.sessionID)
	_, err := a.request(methodViewUnsubscribe, map[string]string{"sessionId": old}, a.APITimeout(), nil)
	return a.sessionID, err
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
	return err
}
