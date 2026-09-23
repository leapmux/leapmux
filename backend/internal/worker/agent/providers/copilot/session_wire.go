package copilot

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// copilotSessionConfig preserves the configured workspace features when the native SDK opens a session.
type copilotSessionConfig struct {
	SessionID                      string `json:"sessionId"`
	ClientName                     string `json:"clientName"`
	WorkingDirectory               string `json:"workingDirectory"`
	Model                          string `json:"model,omitempty"`
	ReasoningEffort                string `json:"reasoningEffort,omitempty"`
	EnableConfigDiscovery          bool   `json:"enableConfigDiscovery"`
	EnableSkills                   bool   `json:"enableSkills"`
	EnableSessionStore             bool   `json:"enableSessionStore"`
	RequestExtensions              bool   `json:"requestExtensions"`
	RequestPermission              bool   `json:"requestPermission"`
	RequestUserInput               bool   `json:"requestUserInput"`
	RequestExitPlanMode            bool   `json:"requestExitPlanMode"`
	RequestElicitation             bool   `json:"requestElicitation"`
	Streaming                      bool   `json:"streaming"`
	IncludeSubAgentStreamingEvents bool   `json:"includeSubAgentStreamingEvents"`
	ContinuePendingWork            *bool  `json:"continuePendingWork,omitempty"`
}

type copilotSessionInfo struct {
	SessionID    string          `json:"sessionId"`
	Mode         string          `json:"mode"`
	ModelState   json.RawMessage `json:"modelState"`
	Capabilities struct {
		UI struct {
			Elicitation bool `json:"elicitation"`
		} `json:"ui"`
	} `json:"capabilities"`
}

func newCopilotSessionConfig(opts agent.Options, sessionID string, resume bool) copilotSessionConfig {
	config := copilotSessionConfig{
		SessionID: sessionID, ClientName: "leapmux", WorkingDirectory: opts.WorkingDir,
		Model: opts.Model(), ReasoningEffort: opts.Effort(),
		EnableConfigDiscovery: true, EnableSkills: true, EnableSessionStore: true, RequestExtensions: true,
		RequestPermission: true, RequestElicitation: true,
		Streaming: true, IncludeSubAgentStreamingEvents: true,
	}
	// Explicit event interests retain tool IDs that the SDK's question and plan callbacks omit.
	// The caller registers those interests before it sends input.
	if agent.UsesAccountDefaultModel(config.Model) {
		config.Model = ""
	}
	// EffortAuto is the effort-side counterpart of the account-default model above: it
	// means "send no effort at all", and the runtime then keeps the tier its own model
	// offers. The runtime accepts the tiers of its catalogue alone, so the sentinel
	// itself must never reach the wire.
	if config.ReasoningEffort == agent.EffortAuto {
		config.ReasoningEffort = ""
	}
	if resume {
		// The default interrupts pending work during resume.
		// Request continuation where the runtime supports it.
		// Pending permissions can reappear. Copilot 1.0.83 interrupts suspended questions.
		// Do not suppress session.resume: that event drives pending-work recovery.
		value := true
		config.ContinuePendingWork = &value
	}
	return config
}

// openSession checks the response identity before its caller accepts session state.
// The caller registers the requested identity before this call because startup can produce session events.
func (c *copilotConnection) openSession(opts agent.Options, sessionID string, resume bool, timeout time.Duration) (copilotSessionInfo, error) {
	method := "session.create"
	if resume {
		method = "session.resume"
	}
	config := newCopilotSessionConfig(opts, sessionID, resume)
	return c.sendNativeSessionConfig(method, config, timeout)
}

func (c *copilotConnection) sendNativeSessionConfig(method string, config copilotSessionConfig, timeout time.Duration) (copilotSessionInfo, error) {
	if config.SessionID == "" {
		return copilotSessionInfo{}, fmt.Errorf("the Copilot session ID is empty")
	}
	params, err := json.Marshal(config)
	if err != nil {
		return copilotSessionInfo{}, fmt.Errorf("encode Copilot session configuration: %w", err)
	}
	raw, err := c.SendRequest(method, params, timeout)
	if err != nil {
		return copilotSessionInfo{}, err
	}
	var session copilotSessionInfo
	if err := json.Unmarshal(raw, &session); err != nil {
		return copilotSessionInfo{}, fmt.Errorf("decode Copilot session: %w", err)
	}
	if session.SessionID != config.SessionID {
		return copilotSessionInfo{}, fmt.Errorf("the Copilot response has a different session ID: expected %q, received %q", config.SessionID, session.SessionID)
	}
	return session, nil
}
