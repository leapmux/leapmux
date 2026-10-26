package fastagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func (a *Agent) clearProviderState() {
	a.clearChildState()
	a.rootMu.Lock()
	a.rootSession, a.rootSeen, a.rootLive = "", nil, nil
	a.rootPromptSeen, a.rootBaselineErr = false, nil
	a.rootMu.Unlock()
}

func (a *Agent) initializeRootArchive(resumed bool) error {
	if !a.nativeCommands {
		return nil
	}
	sessionID := a.CurrentSessionID()
	var tools []fastagentRootTool
	if resumed {
		histories, err := readFastagentRootArchive(a.home, sessionID, a.workingDir)
		if err != nil {
			return fmt.Errorf("read the resumed Fast Agent command archive: %w", err)
		}
		tools, err = fastagentRootTools(sessionID, histories)
		if err != nil {
			return err
		}
	}
	a.rootMu.Lock()
	a.rootSession = sessionID
	a.rootSeen = make(map[string]fastagentRootTool, len(tools))
	a.rootLive = make(map[string]struct{})
	a.rootPromptSeen, a.rootBaselineErr = resumed, nil
	for _, tool := range tools {
		a.rootSeen[tool.id] = tool
	}
	a.rootMu.Unlock()
	return nil
}

// Retire the native archive prefix before this prompt. A failed prior read
// must not move that prior turn's tools into this turn's transcript.
func (a *Agent) prepareRootArchive() {
	if !a.nativeCommands {
		return
	}
	sessionID := a.CurrentSessionID()
	a.rootMu.Lock()
	if a.rootClosing {
		a.rootMu.Unlock()
		return
	}
	first := a.rootSession != sessionID || !a.rootPromptSeen
	if a.rootSession != sessionID {
		a.rootSession = sessionID
		a.rootSeen = make(map[string]fastagentRootTool)
		a.rootLive = make(map[string]struct{})
	}
	a.rootPromptSeen, a.rootBaselineErr = true, nil
	// Live UUIDs belong only to this turn and can be reused by later tools.
	a.rootLive = make(map[string]struct{})
	a.rootMu.Unlock()
	histories, err := readFastagentRootArchive(a.home, sessionID, a.workingDir)
	// Fast Agent creates no archive until the first prompt of a fresh session.
	if first && errors.Is(err, os.ErrNotExist) {
		return
	}
	var tools []fastagentRootTool
	if err == nil {
		tools, err = fastagentRootTools(sessionID, histories)
	}
	a.rootMu.Lock()
	defer a.rootMu.Unlock()
	if a.rootClosing || a.rootSession != sessionID {
		return
	}
	if err != nil {
		a.rootBaselineErr = fmt.Errorf("capture the fastagent archive before the prompt: %w", err)
		return
	}
	seen := make(map[string]fastagentRootTool, len(tools))
	for _, tool := range tools {
		if previous, exists := a.rootSeen[tool.id]; exists && !tool.matchesRecorded(previous) {
			a.rootBaselineErr = fmt.Errorf("fastagent command identity %q has conflicting archive records", tool.nativeID)
			return
		}
		seen[tool.id] = tool
	}
	a.rootSeen = seen
}

// Live shell packets omit the archive's arguments, results and model call ID.
// Consume only root native commands; children and archive replays keep their route.
func (a *Agent) observeRootTool(sessionID string, _ agent.ProviderServices, raw json.RawMessage) bool {
	if !a.nativeCommands || sessionID != a.CurrentSessionID() {
		return false
	}
	var update struct {
		Kind  string                     `json:"sessionUpdate"`
		ID    string                     `json:"toolCallId"`
		Title string                     `json:"title"`
		Meta  map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &update) != nil || update.ID == "" ||
		(update.Kind != "tool_call" && update.Kind != "tool_call_update") {
		return false
	}
	if _, archive := update.Meta[contracts.FastagentArchiveNamespace]; archive {
		return false
	}
	var parent string
	if json.Unmarshal(update.Meta["parentToolCallId"], &parent) == nil && parent != "" {
		return false
	}
	a.rootMu.Lock()
	defer a.rootMu.Unlock()
	if a.rootSession != sessionID {
		return false
	}
	if update.Title == contracts.FastagentToolBash || update.Title == contracts.FastagentToolProcess {
		a.rootLive[update.ID] = struct{}{}
	}
	_, suppressed := a.rootLive[update.ID]
	return suppressed
}

func (a *Agent) finishPrompt(err error, stopped bool) acp.PromptEnding {
	// Detached prompt callbacks can run after the native process exits. Register
	// before cleanup closes admission, so Wait can join every admitted callback.
	a.rootMu.Lock()
	if a.rootClosing {
		a.rootMu.Unlock()
		return acp.PromptEnding{}
	}
	a.rootArchiveWG.Add(1)
	a.rootMu.Unlock()
	defer a.rootArchiveWG.Done()
	if a.nativeCommands {
		if replayErr := a.replayRootArchive(); replayErr != nil {
			slog.Warn("Fast Agent command archive is unavailable", "agent_id", a.AgentID(), "error", replayErr)
			a.Sink().PersistLeapMuxNotification(map[string]interface{}{
				contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
				contracts.NotificationFieldText: "Fast Agent command results could not be read from the native archive: " + replayErr.Error(),
			})
		}
	}
	return a.retryChildArchives(err, stopped)
}

func (a *Agent) finishArchiveReads() {
	a.rootMu.Lock()
	a.rootClosing = true
	a.rootMu.Unlock()
	a.rootArchiveWG.Wait()
	a.archiveWG.Wait()
}

// PromptEnded holds the base's session lock until the turn ends. Replaying
// synchronously keeps archive results before that end and before queued input.
func (a *Agent) replayRootArchive() error {
	sessionID := a.CurrentSessionID()
	a.rootMu.Lock()
	baselineErr := a.rootBaselineErr
	a.rootMu.Unlock()
	if baselineErr != nil {
		return baselineErr
	}
	histories, err := readFastagentRootArchive(a.home, sessionID, a.workingDir)
	if err != nil {
		return err
	}
	tools, err := fastagentRootTools(sessionID, histories)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		a.rootMu.Lock()
		if a.rootSession != sessionID {
			a.rootMu.Unlock()
			return nil
		}
		previous, seen := a.rootSeen[tool.id]
		a.rootMu.Unlock()
		if seen {
			if !tool.matchesRecorded(previous) {
				return fmt.Errorf("fastagent command identity %q has conflicting archive records", tool.nativeID)
			}
			continue
		}
		updates, err := tool.updates()
		if err != nil {
			return err
		}
		for _, update := range updates {
			packet, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
			if err != nil {
				return err
			}
			// Dispatch to the concrete base, not through the provider registry.
			(*acp.Base).HandleOutput(&a.Base, packet)
		}
		a.rootMu.Lock()
		if a.rootSession == sessionID {
			a.rootSeen[tool.id] = tool
		}
		a.rootMu.Unlock()
	}
	return nil
}
