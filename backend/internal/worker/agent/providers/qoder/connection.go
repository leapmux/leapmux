package qoder

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// SendRawInput supplies the session routing that Qoder's SDK daemon requires.
// Daemon requests have connection scope. A native control answer may already
// carry the session that raised it, which must stay unchanged.
func (a *Agent) SendRawInput(input agent.RawInput) error {
	raw, stop := input.Data, input.Stop
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil || frame == nil {
		return fmt.Errorf("the Qoder input frame must be a JSON object")
	}
	method := providerkit.StringField(frame["request"], "subtype")
	if method == "" {
		method = providerkit.StringField(frame["request"], "type")
	}
	if !strings.HasPrefix(method, "daemon.") && providerkit.StringField(raw, "session_id") == "" {
		if sessionID := a.nativeSessionID(); sessionID != "" {
			encoded, err := json.Marshal(sessionID)
			if err != nil {
				return err
			}
			frame["session_id"] = encoded
		}
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return a.Process.SendRawInput(encoded, stop)
}

// startDaemon opens one session on a private SDK daemon. Ordinary headless
// stream-json input ignores control requests in Qoder 1.1.86.
func (a *Agent) startDaemon(workerArgs []string) error {
	auth := map[string]any{"type": "qodercli"}
	if a.credentials.jobToken != "" {
		auth = map[string]any{"type": "jobToken", "jobTokenProvider": "host"}
	} else if a.credentials.serviceAccountKey != "" {
		auth = map[string]any{"type": "serviceAccount", "serviceAccountKey": a.credentials.serviceAccountKey}
	}
	request, err := json.Marshal(map[string]any{
		"subtype":                 "daemon.initialize",
		"daemon_protocol_version": 2,
		"sdk_version":             "leapmux",
		"capabilities":            []string{"multi_session_v2"},
		"auth":                    auth,
	})
	if err != nil {
		return err
	}
	initialized, err := a.sendControlAndWait(string(request), initializeTimeout)
	if err != nil {
		return fmt.Errorf("initialize the Qoder daemon: %w", err)
	}
	var protocol struct {
		Version      int      `json:"daemon_protocol_version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(initialized.RawResponse, &protocol); err != nil || protocol.Version != 2 || !slices.Contains(protocol.Capabilities, "multi_session_v2") {
		return fmt.Errorf("the Qoder daemon did not confirm protocol version 2 with multi_session_v2")
	}
	// initialize queues authentication but does not wait for it. Opening a
	// session first lets custom model selection run before its access check.
	authenticated, err := a.sendControlAndWait(`{"subtype":"daemon.authenticate"}`, initializeTimeout)
	if err != nil {
		return fmt.Errorf("authenticate the Qoder daemon: %w", err)
	}
	var identity struct {
		Authenticated bool `json:"authenticated"`
	}
	if json.Unmarshal(authenticated.RawResponse, &identity) != nil || !identity.Authenticated {
		return fmt.Errorf("the Qoder daemon has no authenticated identity")
	}
	if err := a.readCustomModelCatalog(); err != nil {
		return err
	}
	request, err = json.Marshal(map[string]any{
		"subtype":     "daemon.open_session",
		"cwd":         a.opts.WorkingDir,
		"worker_args": workerArgs,
	})
	if err != nil {
		return err
	}
	opened, err := a.sendControlAndWait(string(request), initializeTimeout)
	if err != nil {
		return fmt.Errorf("open the Qoder session: %w", err)
	}
	if opened.SessionID == "" {
		return fmt.Errorf("the Qoder daemon did not identify the opened session")
	}
	a.mu.Lock()
	a.sessionID = opened.SessionID
	a.mu.Unlock()
	return a.startStream()
}

// answerDaemonAuthentication answers the SDK host callback without storing a credential.
func (a *Agent) answerDaemonAuthentication(raw []byte) bool {
	var request controlRequestEnvelope
	if json.Unmarshal(raw, &request) != nil || request.RequestID == "" {
		return false
	}
	method := providerkit.StringField(request.Request, "type")
	if method == "" {
		method = providerkit.StringField(request.Request, "subtype")
	}
	if method != "daemon.auth.fetch_job_token" {
		return false
	}
	response := map[string]any{"subtype": "error", "request_id": request.RequestID, "error": "No Qoder job token is configured"}
	if a.credentials.jobToken != "" {
		credential := map[string]any{"token": a.credentials.jobToken}
		if a.credentials.jobTokenExpiresAt != nil {
			credential["expires_at"] = *a.credentials.jobTokenExpiresAt
		}
		response = map[string]any{"subtype": "success", "request_id": request.RequestID, "response": credential}
	}
	encoded, err := json.Marshal(map[string]any{"type": frameTypeControlResponse, "response": response})
	if err == nil {
		// Authentication callbacks have connection scope, including after a session opens.
		err = a.Process.SendRawInput(encoded, agent.StopContext{})
	}
	if err != nil {
		slog.Warn("could not answer Qoder authentication", "agent_id", a.AgentID(), "error", err)
	}
	return true
}
