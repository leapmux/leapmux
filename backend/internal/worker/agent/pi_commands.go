package agent

import (
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"time"
)

// refreshPiCommands supplies both command dispatch and optional extension capabilities.
//
// It reports whether the agent holds a catalog now. Goal control and the
// extension-command dispatch both read that catalog, so a read that failed leaves
// both switched off until a later read succeeds. The caller decides when to ask
// again, and refreshPiGoalControl is the path that does.
func (a *PiAgent) refreshPiCommands(timeout time.Duration) bool {
	raw, err := a.sendPiCommand(PiCommandGetCommands, nil, timeout)
	if err != nil {
		slog.Warn("read Pi extension commands", "agent_id", a.agentID, "error", err)
		return false
	}
	var catalog struct {
		Commands *[]struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		slog.Warn("read Pi command catalog", "agent_id", a.agentID, "error", err)
		return false
	}
	if catalog.Commands == nil {
		slog.Warn("Pi command catalog has no command list", "agent_id", a.agentID)
		return false
	}
	commands := make(map[string]bool)
	for _, command := range *catalog.Commands {
		if command.Source == "extension" && command.Name != "" {
			commands[command.Name] = true
		}
	}
	a.mu.Lock()
	changed := !maps.Equal(a.extensionCommands, commands)
	a.extensionCommands = commands
	a.mu.Unlock()
	if changed {
		a.sink.PublishGoalCapabilities()
	}
	return true
}

// refreshPiGoalControl re-reads the extension catalog, then asks for a goal
// snapshot. A replacement session can load a different extension set, and the
// catalog is what decides whether goal control exists at all -- so the two belong
// together at every point where the session identity changes.
//
// It sends an RPC, so every caller runs it on its own goroutine: the Pi read loop
// must stay free to deliver the response.
func (a *PiAgent) refreshPiGoalControl() {
	a.refreshPiCommands(a.APITimeout())
	a.schedulePiGoalRefresh(true)
}

func (a *PiAgent) isPiExtensionCommand(message string) bool {
	if !strings.HasPrefix(message, "/") {
		return false
	}
	// Pi splits the command at the first space and passes the remaining text unchanged.
	command, _, _ := strings.Cut(message[1:], " ")
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.extensionCommands[command]
}
