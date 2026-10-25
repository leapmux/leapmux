package copilot

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Folder trust.
//
// Copilot loads the MCP servers of a project (`.mcp.json`) only from a folder in
// its trusted list, which it shares with the Copilot CLI. The CLI asks about a
// folder before it starts; in server mode Copilot asks nothing, and leaves the
// question to its host. LeapMux asks through providerkit.WorkspaceTrust: after a
// session opens, a folder that Copilot does not trust gets one question. Trust
// adds the folder to Copilot's list and reloads the session's MCP servers, so they
// load without a restart. Instructions, custom agents and skills load either way,
// as in Copilot's own policy, so the question names the MCP servers alone.

// copilotTrustedConfiguration is what Copilot's folder trust controls, as the
// question states it.
var copilotTrustedConfiguration = []string{"MCP servers"}

// copilotWorkspaceTrustHost is Copilot's own trusted-folder list. The caller
// holds sessionMu for reading.
type copilotWorkspaceTrustHost struct{ a *Agent }

var _ providerkit.WorkspaceTrustHost = copilotWorkspaceTrustHost{}

// WorkspaceTrusted asks Copilot whether it trusts the working directory.
func (h copilotWorkspaceTrustHost) WorkspaceTrusted() (bool, error) {
	raw, err := h.a.requestNativeSession("permissions.folderTrust.isTrusted", map[string]any{"path": h.a.opts.WorkingDir})
	if err != nil {
		return false, err
	}
	var answer struct {
		Trusted *bool `json:"trusted"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.Trusted == nil {
		return false, fmt.Errorf("copilot answered the folder trust without a verdict")
	}
	return *answer.Trusted, nil
}

// TrustWorkspace adds the working directory to Copilot's trusted folders, loads
// the project's MCP servers in the running session, and loads the repository
// hooks that Copilot deferred until the trust.
func (h copilotWorkspaceTrustHost) TrustWorkspace() error {
	a := h.a
	raw, err := a.requestNativeSession("permissions.folderTrust.addTrusted", map[string]any{"path": a.opts.WorkingDir})
	if err != nil {
		return fmt.Errorf("trust the folder in Copilot: %w", err)
	}
	var receipt struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Success == nil || !*receipt.Success {
		return fmt.Errorf("copilot did not add the folder %s to its trusted folders", a.opts.WorkingDir)
	}
	// The folder is trusted now, so a failure below only delays what loads: the
	// next session that opens in the folder loads it.
	if _, err := a.requestNativeSession("mcp.reload", nil); err != nil {
		slog.Warn("Reload the Copilot MCP servers after the folder trust", "agent_id", a.AgentID(), "error", err)
	}
	params, err := json.Marshal(map[string]string{"sessionId": a.currentNativeSessionID()})
	if err == nil {
		_, err = a.SendRequest("sessions.loadDeferredRepoHooks", params, a.APITimeout())
	}
	if err != nil {
		slog.Warn("Load the deferred Copilot repository hooks", "agent_id", a.AgentID(), "error", err)
	}
	return nil
}

// checkFolderTrustInBackground asks about the working directory off the reader
// goroutine, after a session opened. offReader states why the requests cannot run
// on the reader, and it waits for a session replacement that holds sessionMu.
func (a *Agent) checkFolderTrustInBackground() {
	a.offReader(copilotCheckFolderTrust, a.checkFolderTrust)
}

// checkFolderTrust asks the reader to trust the working directory when Copilot
// does not trust it. The caller holds sessionMu for reading.
func (a *Agent) checkFolderTrust() {
	a.workspaceTrust.Ask(a.sink, copilotWorkspaceTrustHost{a}, a.currentNativeSessionID(), a.opts.WorkingDir, copilotTrustedConfiguration)
}
