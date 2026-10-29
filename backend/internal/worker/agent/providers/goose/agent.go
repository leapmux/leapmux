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

// gooseSteeringMethod supplies the native request even though Goose does not
// advertise it. Goose 1.56.0 accepts the request and reports its active run
// through session metadata. A removed method returns -32601, which SteerInput
// maps to ErrSteeringUnsupported so the input returns to the queue.
func gooseSteeringMethod([]byte) string {
	return gooseSteerMethod
}

// Agent manages a single Goose CLI ACP process.
type Agent struct {
	acp.Base
	gooseOutput map[string]gooseOutputState
	// goalArmed is set by the goal notice and consumed at PromptEnded.
	goalArmed     bool
	goalObjective string
	// The last agent message of the running prompt, which the goal-end rule
	// and the turn-failure verdict read (see observeGooseSessionUpdate).
	// lastMessageInference states that a chunk of the message carried
	// `_meta.goose.inference`, the model that wrote it. Goose's own error
	// messages never carry it.
	// lastMessageUpdate is the raw update of its last chunk. PromptPrepared
	// and PromptEnded clear all four (takeGooseLastMessage).
	lastMessageID        string
	lastMessageText      string
	lastMessageInference bool
	lastMessageUpdate    json.RawMessage
	// compaction marks the running prompt when it runs Goose's compaction, and
	// is nil for every other prompt. The compaction prompt's preparation sets
	// it, and the prompt's end takes it (finishGoosePrompt). Guarded by Mu.
	compaction *gooseCompactionMark
	// nativeCommands states the launch value of the Native Commands axis.
	// Goose reads the terminal capability only in the initialize handshake,
	// so a change restarts the process.
	nativeCommands bool
	// gooseChildRenames remembers each delegate ack's row rename, from the
	// spawn toolCallId to the native session id the ack stated. A tool request
	// that arrives after the ack still names the spawn id, so it must reach
	// the renamed row.
	gooseChildRenames map[string]string
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerPreparer = (*Agent)(nil)

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
		if providerkit.HasJSONRPCErrorCode(err, -32601) {
			return agent.ErrSteeringUnsupported
		}
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
