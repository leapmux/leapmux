package muse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native host and the root and child sessions that the host serves.
type Agent struct {
	*connection
	sink       agent.ProviderServices
	opts       agent.Options
	sendMu     sync.Mutex
	dispatchMu sync.Mutex
	// deferredGoalActions holds goal writes queued under dispatchMu that run
	// after dispatch releases. See handleOutput.
	deferredGoalActions []func()
	finalizeMu          sync.Mutex
	stateMu             sync.Mutex
	controlMu           sync.Mutex
	controlQueue        []*controlRecord
	controlDraining     bool
	sessionID           string
	settings            optionmap.Map
	models              []*agent.ModelInfo
	sessions            map[string]*sessionState
}

type sessionState struct {
	controls        map[string]controlState
	sink            agent.ProviderServices
	turnID          string
	items           map[string]*itemState
	itemIndex       *nativeItemIndex
	completed       map[string]bool
	log             *nativeLog
	toolUses        int32
	generation      providerkit.GenerationBuffer
	childKey        string
	childID         string
	parentSessionID string
	subagentID      string
	retired         bool
	finalizations   []*turnFinalization
	nextItemOrder   uint64
	viewRecovery    *viewRecovery
	viewTwins       map[string]bool
	contextUsage    *nativeContextUsage
	tokenUsage      *nativeTokenUsage
}

type itemState struct {
	params              itemParams
	raw                 []byte
	opened              bool
	persistedRevision   int64
	deltas              map[string]string
	cursors             map[string]bool
	order               uint64
	generationScopes    []string
	lastGenerationField string
	generationPart      uint64
}

type inputPart struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Base64Data string `json:"base64Data,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

func inputParts(text string, attachments []*leapmuxv1.Attachment) ([]inputPart, error) {
	parts := []inputPart{{Type: "text", Text: text}}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if err := (museProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		switch attachment.Kind {
		case agent.AttachmentKindText:
			parts = append(parts, inputPart{Type: "text", Text: providerkit.BuildInlineTextAttachmentBlock(attachment)})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			return nil, fmt.Errorf("the Muse MSP input does not accept %s attachments", attachment.Kind)
		case agent.AttachmentKindImage:
			parts = append(parts, inputPart{Type: "image", MediaType: attachment.MIMEType, Base64Data: base64.StdEncoding.EncodeToString(attachment.Data)})
		}
	}
	return parts, nil
}

func (a *Agent) PublishTurnActive() agent.TurnState {
	a.stateMu.Lock()
	state := a.sessions[a.sessionID]
	active := state != nil && state.turnID != ""
	a.Mu.Lock()
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.stateMu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, seq)
}

func (a *Agent) SendInput(text string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, text, attachments, false)
}
func (a *Agent) SendInputForSession(id, text string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&id, text, attachments, false)
}
func (a *Agent) SteerInput(text string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, text, attachments, true)
}
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) sendInput(expected *string, text string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.stateMu.Lock()
	id := a.sessionID
	state := a.sessions[id]
	turn := ""
	if state != nil {
		turn = state.turnID
	}
	a.stateMu.Unlock()
	if err := providerkit.CheckInputSession(expected, id); err != nil {
		return err
	}
	if a.IsStopped() {
		return fmt.Errorf("the Muse agent is stopped")
	}
	if steer && turn == "" {
		return agent.ErrNoActiveTurn
	}
	if !steer && turn != "" {
		return agent.ErrAgentBusy
	}
	parts, err := inputParts(text, attachments)
	if err != nil {
		return err
	}
	params := map[string]any{"sessionId": id, "input": parts}
	method := methodTurnStart
	if steer {
		method = methodTurnSteer
		params["expectedTurnId"] = turn
	}
	_, err = a.command(method, params, a.APITimeout(), func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var reply turnResult
		if json.Unmarshal(raw, &reply) != nil || reply.TurnID == "" {
			return
		}
		if !steer {
			a.startTurn(id, reply.TurnID)
		}
	})
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	return nil
}

func (a *Agent) HandleOutput(raw []byte) { a.handleOutput(providerkit.ParseLine(raw)) }
func (a *Agent) Stop() {
	_ = a.close()
	a.retireHost(a.ProcessExitCompletion())
}
func (a *Agent) Wait() error {
	err := a.wait()
	a.retireHost(a.ProcessExitCompletion())
	return err
}
