package muse

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native host and the root and child sessions that the host serves.
type Agent struct {
	*connection
	sink                   agent.ProviderServices
	opts                   agent.Options
	sendMu                 sync.Mutex
	dispatcher             *outputDispatcher
	stateMu                sync.Mutex
	controlMu              sync.Mutex
	controlQueue           []*controlRecord
	controlDraining        bool
	approvalWork           providerkit.CoalescingWork
	viewWork               providerkit.CoalescingWork
	logRecoveryWork        providerkit.CoalescingWork
	subscriptionWork       providerkit.CoalescingWork
	sessionID              string
	settings               optionmap.Map
	models                 []*agent.ModelInfo
	sessions               map[string]*sessionState
	uncertainInput         *museUncertainInput           // sendMu protects the pending native command identity.
	uncertainChildCommands map[string]museUncertainInput // sendMu protects child command identities.
	// manualCompactionSession is the root session whose manual compaction
	// CompactContext started and whose end has not applied its retry verdict
	// yet. It is empty when no manual compaction is pending. The first end of
	// that compaction takes it: the noop acknowledgement, the end of the
	// compaction item, or the retirement of the session. stateMu guards it.
	manualCompactionSession string
}

type museUncertainInput struct {
	sessionID string
	digest    string
	commandID string
}

// A native completion can precede transcript projection. The observed state
// retains its turn ownership until the immutable finalization is captured.
type turnCompletionState uint8

const (
	turnCompletionUnobserved turnCompletionState = iota
	turnCompletionObserved
	turnCompletionCaptured
)

type sessionState struct {
	subscriptionLock chan struct{}
	controls         map[string]controlState
	sink             agent.ProviderServices
	turnID           string
	// stopRequestedTurnID identifies the exact turn the user asked to stop.
	// Finalization captures this intent even when the native failure wins.
	stopRequestedTurnID  string
	items                map[string]*itemState
	itemIndex            *nativeItemIndex
	completed            map[string]turnCompletionState
	log                  *nativeLog
	toolUses             int32
	generation           providerkit.GenerationBuffer
	retry                providerkit.RetryResolution
	retryItems           map[string]bool
	retryNotices         []agent.CapturedTranscript
	compactionLive       map[string]bool
	childKey             string
	childID              string
	parentSessionID      string
	subagentID           string
	retired              bool
	finalizations        []*turnFinalization
	nextItemOrder        uint64
	viewRecovery         *viewRecovery
	viewOverlap          map[[32]byte]struct{}
	childViewRequested   bool
	childViewPrefix      childViewPrefixState
	acceptedViewCursor   *string
	projection           *childProjection
	openingPrompt        *childOpeningPrompt
	nativeTurn           childNativeTurnState
	nativeHead           *childNativeHead
	settledChildControls map[string]*childControlSettlement
	contextUsage         *nativeContextUsage
	tokenUsage           *nativeTokenUsage
}

// acquireSessionSubscription serializes registration and cleanup for one session.
// The channel wait also ends when the owning native process stops.
func (a *Agent) acquireSessionSubscription(state *sessionState) (func(), error) {
	return a.acquireSessionSubscriptionContext(a.Context(), state)
}

func (a *Agent) acquireSessionSubscriptionContext(ctx context.Context, state *sessionState) (func(), error) {
	if state == nil {
		return nil, agent.ErrChildRouteNotReady
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.stateMu.Lock()
	if state.subscriptionLock == nil {
		state.subscriptionLock = make(chan struct{}, 1)
	}
	slot := state.subscriptionLock
	a.stateMu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-a.Context().Done():
		return nil, a.Context().Err()
	}
	if err := ctx.Err(); err != nil {
		<-slot
		return nil, err
	}
	if err := a.Context().Err(); err != nil {
		<-slot
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { <-slot }) }, nil
}

type itemState struct {
	params        itemParams
	raw           []byte
	nativeItemRaw json.RawMessage
	registry      *registryPublication
	opened        bool
	// retainedRevision includes writes that the shared queue owns for retry.
	retainedRevision     int64
	retainedWriteReceipt *agent.TranscriptWriteReceipt
	retainedPublication  *nativeToolPublication
	deltas               map[string]string
	cursors              map[string]bool
	order                uint64
	generationScopes     []string
	lastGenerationField  string
	generationPart       uint64
}

// nativeObject returns the unchanged item object from its stored native source.
// A metadata-only item retains that object until a genuine frame arrives.
func (item *itemState) nativeObject() json.RawMessage {
	if len(item.nativeItemRaw) > 0 {
		return item.nativeItemRaw
	}
	var frame struct {
		Params struct {
			Item json.RawMessage `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(item.raw, &frame) != nil {
		return nil
	}
	return frame.Params.Item
}

type inputPart struct {
	Type       string  `json:"type"`
	Text       *string `json:"text,omitempty"`
	Base64Data string  `json:"base64Data,omitempty"`
	MediaType  string  `json:"mediaType,omitempty"`
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

func inputParts(text string, attachments []*leapmuxv1.Attachment) ([]inputPart, error) {
	parts := []inputPart{{Type: "text", Text: &text}}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if err := (museProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		switch attachment.Kind {
		case contracts.AttachmentMediaKindText:
			attachmentText := providerkit.BuildInlineTextAttachmentBlock(attachment)
			parts = append(parts, inputPart{Type: "text", Text: &attachmentText})
		case contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary, contracts.AttachmentMediaKindAudio:
			return nil, fmt.Errorf("the Muse MSP input does not accept %s attachments", attachment.Kind)
		case contracts.AttachmentMediaKindImage:
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
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode the Muse input: %w", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	payload, commandID, err := commandParams(params)
	if err != nil {
		return err
	}
	if prior := a.uncertainInput; !steer && prior != nil && prior.sessionID == id && prior.digest == digest {
		commandID = prior.commandID
	}
	a.uncertainInput = nil
	_, err = a.commandWithID(method, payload, commandID, a.APITimeout(), func(raw json.RawMessage, err error) {
		if err != nil {
			return
		}
		var reply turnResult
		if json.Unmarshal(raw, &reply) != nil || reply.TurnID == "" {
			return
		}
		if !steer && (reply.Disposition == museDispositionStarted || reply.StartedNewTurn) {
			a.startTurn(id, reply.TurnID)
		}
	})
	if err != nil {
		err = providerkit.ClassifyJSONRPCDeliveryError(method, err)
		if !steer && errors.Is(err, agent.ErrDeliveryUncertain) {
			a.uncertainInput = &museUncertainInput{sessionID: id, digest: digest, commandID: commandID}
		}
		return err
	}
	return nil
}

func (a *Agent) HandleOutput(raw []byte) { a.handleOutput(providerkit.ParseLine(raw)) }
func (a *Agent) Stop() {
	_ = a.close()
	a.subscriptionWork.CloseAndWait()
	a.logRecoveryWork.CloseAndWait()
	a.viewWork.CloseAndWait()
	a.retireHost(a.ProcessExitCompletion())
}
func (a *Agent) Wait() error {
	err := a.wait()
	a.subscriptionWork.CloseAndWait()
	a.logRecoveryWork.CloseAndWait()
	a.viewWork.CloseAndWait()
	a.retireHost(a.ProcessExitCompletion())
	return err
}
