package commandcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Agent owns one native Command Code RPC session and its private output files.
type Agent struct {
	providerkit.JSONRPCProcess
	sink       agent.ProviderServices
	sendMu     sync.Mutex
	dispatchMu sync.Mutex
	// replyProcessID separates durable reply keys when a process reuses a native turn ID.
	replyProcessID string
	// dispatchMu guards reply capture and the turn identified by the native output stream.
	replyOutputTurnID string
	replies           providerkit.NativeReplyLedger
	pendingSteers     []*pendingCommandCodeSteer
	// dispatchMu guards the error while the next native row determines whether it repeats.
	pendingRunError *agent.CapturedTranscript
	clock           quartz.Clock
	runtimeDir      string
	bridgeSecret    string
	bridge          *providerkit.HTTPEndpoint
	cleanupOnce     sync.Once
	outputDone      chan struct{}
	workingDir      string
	homeDir         string

	// Mu guards the session, turn, settings, tool and child state below.
	sessionID        string
	model            string
	effort           string
	permissionMode   string
	localOnly        bool
	models           []*agent.ModelInfo
	turnID           string
	steerProofLost   bool
	turnStarted      time.Time
	finishedTurns    []string
	compacting       bool
	compactionLive   bool
	manualCompaction *compactionAttempt
	tools            providerkit.ToolSpanLedger[openTool]
	children         map[string]*childState
	usage            map[string]any
	generation       providerkit.GenerationBuffer
	retryResolution  providerkit.RetryResolution
}

type openTool struct {
	name    string
	input   json.RawMessage
	opening []byte
}

var (
	_ agent.Agent             = (*Agent)(nil)
	_ agent.InputSteerer      = (*Agent)(nil)
	_ agent.ContextCompactor  = (*Agent)(nil)
	_ agent.SteerEchoReporter = (*Agent)(nil)
	_ agent.SteerSurvivor     = (*Agent)(nil)
)

// PublishTurnActive reads and publishes the same state that refuses concurrent input.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.operationActiveLocked()
	steerable := a.turnID != "" && !a.compacting && a.manualCompaction == nil && !a.steerProofLost
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: steerable}, seq)
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendNativeInput(nil, content, attachments)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendNativeInput(&sessionID, content, attachments)
}

func (a *Agent) sendNativeInput(expected *string, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	err := providerkit.CheckInputSession(expected, a.sessionID)
	busy, stopped, sessionID := a.operationActiveLocked(), a.StoppedLocked(), a.sessionID
	a.Mu.Unlock()
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	if stopped {
		return agent.NativeInputReceipt{}, fmt.Errorf("the Command Code agent is stopped")
	}
	if busy {
		return agent.NativeInputReceipt{}, agent.ErrAgentBusy
	}
	blocks, err := inputBlocks(content, agent.ClassifyAttachments(attachments))
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	var receipt agent.NativeInputReceipt
	var wireTurnID string
	a.beginCommandCodeReplyStart()
	defer func() { a.finishCommandCodeReplyStart(wireTurnID, receipt) }()
	result, err := a.request(methodTurnStart, map[string]any{"input": blocks}, a.APITimeout())
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32010) {
			return receipt, agent.ErrAgentBusy
		}
		return receipt, providerkit.ClassifyJSONRPCDeliveryError(methodTurnStart, err)
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	if json.Unmarshal(result, &response) != nil || response.TurnID == "" {
		return receipt, fmt.Errorf("%w: Command Code returned no turn ID", agent.ErrDeliveryUncertain)
	}
	a.startTurn(response.TurnID)
	wireTurnID = response.TurnID
	turnID := a.commandCodeDurableTurnID(wireTurnID)
	receipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: turnID, TurnID: turnID}
	return receipt, nil
}

func inputBlocks(content string, attachments []agent.ClassifiedAttachment) ([]contentBlock, error) {
	blocks := []contentBlock{{Type: "text", Text: &content}}
	for _, attachment := range attachments {
		if err := (commandcodeProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		switch attachment.Kind {
		case contracts.AttachmentMediaKindText:
			text := providerkit.BuildInlineTextAttachmentBlock(attachment)
			blocks = append(blocks, contentBlock{Type: "text", Text: &text})
		case contracts.AttachmentMediaKindImage:
			blocks = append(blocks, contentBlock{Type: "image", Source: &imageSource{Type: "base64", MediaType: attachment.MIMEType, Data: base64.StdEncoding.EncodeToString(attachment.Data)}})
		case contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary, contracts.AttachmentMediaKindAudio:
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
	a.steerProofLost = false
	a.Mu.Unlock()
	a.PublishTurnActive()
}

func (a *Agent) SupportsSteering() bool { return true }

// SteerRowAtEcho waits for turn/steers_delivered before the queue writes the input row.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd reports that Command Code discards its pending steers when the turn ends.
func (*Agent) SteerSurvivesTurnEnd() bool { return false }

func (a *Agent) operationActiveLocked() bool {
	return a.turnID != "" || a.compacting || a.manualCompaction != nil
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	active := a.operationActiveLocked()
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("the Command Code agent is stopped")
	}
	if !active {
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		a.Mu.Lock()
		stillIdle := !a.StoppedLocked() && !a.operationActiveLocked()
		a.Mu.Unlock()
		if !stillIdle {
			return nil
		}
		a.closeUnfinishedTools(agent.MessageCompletionInterrupted)
		a.closeChildrenMatching(bgtask.StatusInterrupted, func(child *childState) bool { return !child.background })
		if a.sink != nil {
			providerkit.ResetSpansAndProgress(a.sink)
		}
		return nil
	}
	_, err := a.request(methodTurnInterrupt, nil, a.APITimeout())
	return err
}

// ClearContext requires a fresh native process because RPC has no reset route.
func (a *Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }
