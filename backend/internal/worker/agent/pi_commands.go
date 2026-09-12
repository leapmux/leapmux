package agent

import (
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"time"
)

// refreshPiCommands supplies both command dispatch and optional extension capabilities.
func (a *PiAgent) refreshPiCommands(timeout time.Duration) {
	raw, err := a.sendPiCommand(PiCommandGetCommands, nil, timeout)
	if err != nil {
		slog.Warn("read Pi extension commands", "agent_id", a.agentID, "error", err)
		return
	}
	var catalog struct {
		Commands *[]struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		slog.Warn("read Pi command catalog", "agent_id", a.agentID, "error", err)
		return
	}
	if catalog.Commands == nil {
		slog.Warn("Pi command catalog has no command list", "agent_id", a.agentID)
		return
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
