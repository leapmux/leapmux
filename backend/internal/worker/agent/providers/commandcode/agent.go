package commandcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native Command Code RPC session and its private output files.
type Agent struct {
	providerkit.JSONRPCProcess
	sink         agent.ProviderServices
	sendMu       sync.Mutex
	dispatchMu   sync.Mutex
	clock        quartz.Clock
	runtimeDir   string
	bridgeSecret string
	bridge       *providerkit.HTTPEndpoint
	cleanupOnce  sync.Once
	outputDone   chan struct{}
	workingDir   string
	homeDir      string

	// Mu guards the session, turn, settings, tool and child state below.
	sessionID        string
	model            string
	effort           string
	permissionMode   string
	localOnly        bool
	models           []*agent.ModelInfo
	turnID           string
	turnStarted      time.Time
	finishedTurns    []string
	compacting       bool
	manualCompaction *compactionAttempt
	tools            map[string]openTool
	children         map[string]*childState
	usage            map[string]any
	generation       providerkit.GenerationBuffer
}

type openTool struct {
	name    string
	input   json.RawMessage
	opening []byte
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

// PublishTurnActive reads and publishes the same state that refuses concurrent input.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.operationActiveLocked()
	steerable := a.turnID != "" && !a.compacting && a.manualCompaction == nil
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: steerable}, seq)
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	err := providerkit.CheckInputSession(expected, a.sessionID)
	busy, stopped := a.operationActiveLocked(), a.StoppedLocked()
	a.Mu.Unlock()
	if err != nil {
		return err
	}
	if stopped {
		return fmt.Errorf("the Command Code agent is stopped")
	}
	if busy {
		return agent.ErrAgentBusy
	}
	blocks, err := inputBlocks(content, agent.ClassifyAttachments(attachments))
	if err != nil {
		return err
	}
	result, err := a.request(methodTurnStart, map[string]any{"input": blocks}, a.APITimeout())
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32010) {
			return agent.ErrAgentBusy
		}
		return providerkit.ClassifyJSONRPCDeliveryError(methodTurnStart, err)
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	if json.Unmarshal(result, &response) != nil || response.TurnID == "" {
		return fmt.Errorf("%w: Command Code returned no turn ID", agent.ErrDeliveryUncertain)
	}
	a.startTurn(response.TurnID)
	return nil
}

func inputBlocks(content string, attachments []agent.ClassifiedAttachment) ([]contentBlock, error) {
	blocks := []contentBlock{{Type: "text", Text: content}}
	for _, attachment := range attachments {
		if err := (commandcodeProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			blocks = append(blocks, contentBlock{Type: "text", Text: providerkit.BuildInlineTextAttachmentBlock(attachment)})
		case agent.AttachmentKindImage:
			blocks = append(blocks, contentBlock{Type: "image", Source: &imageSource{Type: "base64", MediaType: attachment.MIMEType, Data: base64.StdEncoding.EncodeToString(attachment.Data)}})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			return nil, fmt.Errorf("the Command Code RPC cannot send %s attachments", attachment.Kind)
		}
	}
	return blocks, nil
}

func (a *Agent) startTurn(turnID string) {
	if turnID == "" {
		return
	}
	a.Mu.Lock()
	for _, finished := range a.finishedTurns {
		if finished == turnID {
			a.Mu.Unlock()
			return
		}
	}
	if a.turnID == turnID {
		a.Mu.Unlock()
		return
	}
	a.turnID, a.turnStarted, a.TurnToolUses = turnID, a.clock.Now(), 0
	a.Mu.Unlock()
	a.PublishTurnActive()
}

func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) operationActiveLocked() bool {
	return a.turnID != "" || a.compacting || a.manualCompaction != nil
}

// SteerInput uses the native parent-turn queue. Images require a later full turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	active := a.turnID != "" && !a.compacting && a.manualCompaction == nil
	a.Mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != agent.AttachmentKindText {
			return agent.ErrSteeringUnsupported
		}
		content += "\n\n" + providerkit.BuildInlineTextAttachmentBlock(attachment)
	}
	_, err := a.request(methodTurnSteer, map[string]string{"input": content}, a.APITimeout())
	if err == nil {
		return nil
	}
	if providerkit.HasJSONRPCErrorCode(err, -32001) {
		return agent.ErrNoActiveTurn
	}
	return providerkit.ClassifyJSONRPCDeliveryError(methodTurnSteer, err)
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	_, err := a.request(methodTurnInterrupt, nil, a.APITimeout())
	return err
}

// ClearContext requires a fresh native process because RPC has no reset route.
func (a *Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }
