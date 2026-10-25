package kimi

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Workspace trust.
//
// Kimi Code loads a project's MCP servers, skill roots, additional directories
// and instruction sources only from a workspace it trusts, and only its terminal
// UI asks; `kimi web` leaves the question to its client. LeapMux asks through
// providerkit.WorkspaceTrust: after a session opens in a workspace that Kimi does
// not trust, the chat shows one question. Trust grants it
// (`POST /workspaces/{id}/trust`). The session that runs keeps the configuration
// it loaded, though, and only a session that a new process loads reads the
// project, so the agent asks the Worker for a new process
// (NativeTurnRestartRequired), which runs at the next idle point. The Worker
// resumes the session there when it holds a conversation.

// kimiRouteWorkspaces is the collection of workspaces that `kimi web` knows.
const kimiRouteWorkspaces = kimiAPIPrefix + "/workspaces"

// kimiTrustedConfiguration is what Kimi Code's workspace trust controls, as the
// question states it.
var kimiTrustedConfiguration = []string{"MCP servers", "skills", "instructions"}

// kimiWorkspaceTrustHost is Kimi Code's own trust list.
type kimiWorkspaceTrustHost struct{ a *Agent }

var _ providerkit.WorkspaceTrustHost = kimiWorkspaceTrustHost{}

var _ agent.NativeTurnRestarter = (*Agent)(nil)

// kimiWorkspaceTrust is the data of the trust routes.
type kimiWorkspaceTrust struct {
	Trusted *bool `json:"trusted"`
}

// workspacePath returns the trust route of the workspace that the current
// session belongs to. A session states its workspace.
func (h kimiWorkspaceTrustHost) workspacePath() (string, error) {
	a := h.a
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if err := kimiCheckID("session", sessionID); err != nil {
		return "", err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	var session struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := a.api.get(ctx, kimiSessionPath(sessionID, ""), &session); err != nil {
		return "", fmt.Errorf("read the workspace of the Kimi Code session: %w", err)
	}
	if err := kimiCheckID("workspace", session.WorkspaceID); err != nil {
		return "", err
	}
	return kimiRouteWorkspaces + "/" + session.WorkspaceID + "/trust", nil
}

// WorkspaceTrusted asks Kimi Code whether it trusts the session's workspace.
func (h kimiWorkspaceTrustHost) WorkspaceTrusted() (bool, error) {
	path, err := h.workspacePath()
	if err != nil {
		return false, err
	}
	ctx, cancel := h.a.requestContext()
	defer cancel()
	var trust kimiWorkspaceTrust
	if err := h.a.api.get(ctx, path, &trust); err != nil {
		return false, fmt.Errorf("read the Kimi Code workspace trust: %w", err)
	}
	if trust.Trusted == nil {
		return false, errors.New("kimi Code answered the workspace trust without a verdict")
	}
	return *trust.Trusted, nil
}

// TrustWorkspace grants the trust, and asks for a new process, in which the
// session loads the project's configuration.
func (h kimiWorkspaceTrustHost) TrustWorkspace() error {
	a := h.a
	path, err := h.workspacePath()
	if err != nil {
		return err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	var trust kimiWorkspaceTrust
	if err := a.api.post(ctx, path, nil, &trust); err != nil {
		return fmt.Errorf("trust the Kimi Code workspace: %w", err)
	}
	if trust.Trusted == nil || !*trust.Trusted {
		return errors.New("kimi Code did not trust the workspace")
	}
	a.Mu.Lock()
	a.trustRestartRequired = true
	a.Mu.Unlock()
	// The Worker reads the restart at the next idle turn state: now when no turn
	// runs, and at the end of the turn otherwise.
	a.PublishTurnActive()
	return nil
}

// NativeTurnRestartRequired reports that the reader trusted the workspace, so
// the next prompt needs a new process that loads the project's configuration.
func (a *Agent) NativeTurnRestartRequired() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.trustRestartRequired
}

// checkWorkspaceTrust asks the reader to trust the workspace when Kimi Code does
// not trust it. It runs on its own goroutine, after a session opened.
func (a *Agent) checkWorkspaceTrust() {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	a.workspaceTrust.Ask(a.sink, kimiWorkspaceTrustHost{a}, sessionID, a.workingDir, kimiTrustedConfiguration)
}
