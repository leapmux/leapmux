package goose

import (
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	gooseSteerNamespace = "goose"
	gooseSteerMethod    = "_goose/unstable/session/steer"
)

// Agent manages a single Goose CLI ACP process.
type Agent struct {
	acp.Base
	gooseOutput map[string]gooseOutputState
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	method, active, sessionID, runID := a.SteerTarget()
	if method == "" {
		return agent.ErrSteeringUnsupported
	}
	if !active || runID == "" {
		return agent.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]interface{}{
		"sessionId":     sessionID,
		"expectedRunId": runID,
		"prompt":        acp.BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if _, err := a.SendRequest(method, params, a.APITimeout()); err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	if !a.SteerRunActive(runID) {
		return agent.ErrNoActiveTurn
	}
	return nil
}
