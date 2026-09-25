package ohmypi

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// resumeArgs builds the `--resume` argument that reopens a prior omp session, or
// nothing when there is no handle to reopen.
//
// Resume happens at LAUNCH, through omp's own resolver, and not through the
// switch_session command after startup: that command opens an EMPTY session at a
// path that holds no file and answers success, so a handle that identified no
// session would read as a resume.
//
// The value this reads is the handle the worker stored, which is the handle the
// running process reported, not the one OpenAgent validated. So the rule runs
// again here, and the argument is the handle ResolveResumeHandle RETURNS: the
// path rule normalizes as it checks. A handle that fails the rule fails the
// start; see providerkit.ResumeFailedError.
func resumeArgs(resumeSessionID, homeDir string) ([]string, error) {
	if resumeSessionID == "" {
		return nil, nil
	}
	resolved, err := (ompProvider{}).ResolveResumeHandle(resumeSessionID, homeDir)
	if err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored Oh My Pi session handle is not valid: %w", err))
	}
	return []string{"--resume", resolved}, nil
}

// sessionHandleLocked returns the durable session identifier: the session FILE,
// else the session id. The caller holds a.Mu.
//
// The file wins because it resumes in every case. omp creates a session's file
// lazily, at the first assistant message, but it reports the file's path at once,
// and `--resume <path>` opens an empty session at a path that holds no file yet.
// `--resume <id>` instead EXITS when no file holds the id, so an agent restarted
// before its first reply -- a settings change, a worker restart -- would fail to
// start at all.
func (a *Agent) sessionHandleLocked() string {
	if a.sessionFile != "" {
		return a.sessionFile
	}
	return a.sessionID
}

// sessionState takes the fields of a get_state response that the worker reads.
//
// The response is large -- it carries the system prompt and every tool's schema,
// about 100 KB -- so the worker reads it only at startup, after a model switch,
// and after a context clear.
type sessionState struct {
	Model *struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	} `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	SessionID     string `json:"sessionId"`
	SessionFile   string `json:"sessionFile"`
}

// applyState folds a get_state response into the agent: the model, the thinking
// level and the session identity. It reports whether the session identity
// changed.
//
// A reader's thinking level of Auto stays Auto: omp reports the level it
// resolved, and the reader chose omp's own default rather than that level. omp
// states no level for a model that does not reason, which runs "off".
func (a *Agent) applyState(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var state sessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("omp get_state decode failed", "agent_id", a.AgentID(), "error", err)
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	hasModel := state.Model != nil && state.Model.ID != ""
	if hasModel {
		a.model = joinModelID(state.Model.Provider, state.Model.ID)
	}
	level := state.ThinkingLevel
	if level == "" && hasModel {
		level = thinkingOff
	}
	if level != "" {
		a.effectiveThinking = level
		if a.thinkingLevel != agent.EffortAuto {
			a.thinkingLevel = level
		}
	}
	return a.applySessionIdentityLocked(state.SessionID, state.SessionFile)
}

// applySessionIdentityLocked records the session id and file. The caller holds
// a.Mu. It reports whether either changed.
//
// The id and the file identify ONE session, so a new id replaces both: keeping the
// previous file beside a new id would persist a resume handle for the session omp
// just replaced.
func (a *Agent) applySessionIdentityLocked(sessionID, sessionFile string) bool {
	changed := (sessionID != "" && sessionID != a.sessionID) || (sessionFile != "" && sessionFile != a.sessionFile)
	switch {
	case sessionID != "" && sessionID != a.sessionID:
		a.sessionID = sessionID
		a.sessionFile = sessionFile
	case sessionFile != "":
		a.sessionFile = sessionFile
	}
	return changed
}

// ClearContext starts a fresh omp session on the running process.
//
// omp's new_session answers with a cancellation flag alone, so a get_state
// follows it for the new session's file. new_session also clears omp's own
// subagent registry, so the subagents the replaced session ran end here too.
func (a *Agent) ClearContext() (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	raw, err := a.sendCommand(CommandNewSession, nil, a.APITimeout())
	if err != nil {
		return "", err
	}
	var response struct {
		Cancelled *bool `json:"cancelled"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("read the omp new_session response: %w", err)
	}
	if response.Cancelled == nil {
		return "", fmt.Errorf("the omp new_session response has no cancellation status")
	}
	if *response.Cancelled {
		return "", agent.ErrContextClearCancelled
	}
	stateRaw, err := a.sendCommand(CommandGetState, nil, a.APITimeout())
	if err != nil {
		return "", fmt.Errorf("read the new omp session: %w", err)
	}

	root := a.rootConversation()
	a.flushGeneration(root, agent.MessageCompletionInterrupted)
	a.persistIncompleteTools(root, agent.MessageCompletionInterrupted)
	a.closeSubagents(subagentStatusForCompletion(agent.MessageCompletionInterrupted))
	a.closeAllShells()
	a.applyState(stateRaw)
	a.Mu.Lock()
	a.turn.clear()
	a.asks.clearLocked()
	a.usage.reset()
	handle := a.sessionHandleLocked()
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
	// A goal belongs to the session it was set in, and the new session has none.
	a.sink.ClearGoal(false)
	if handle == "" {
		return "", fmt.Errorf("the new omp session has no handle")
	}
	a.sink.UpdateSessionID(handle)
	return handle, nil
}
