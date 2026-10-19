package letta

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one `letta server --listen` process and the one conversation
// LeapMux drives in it.
//
// The process prints two ready lines and then speaks protocol_v2 over one
// WebSocket. The agent embeds Process for the lifecycle and routes
// conversation over the socket (rpc.go), with deltas dispatched in output.go.
type Agent struct {
	providerkit.Process

	sink          agent.ProviderServices
	workingDir    string
	taskLogRoot   string
	taskLogDirect bool
	wsURL         string
	ws            *wsConn
	cancelRuntime context.CancelFunc

	dispatchMu   sync.Mutex
	sendMu       sync.Mutex
	settingsMu   sync.Mutex
	approvalWork providerkit.CoalescingWork
	// The output dispatcher owns held errors and the process-exit boundary.
	heldErrors      map[string][]lettaHeldError
	outputEnded     bool
	finishedTurnIDs []string
	compactionLive  map[string]bool

	// --- guarded by Mu ---

	agentID         string
	conversationID  string
	turnActive      bool
	transportErr    error
	settings        lettaSettings
	catalog         lettaCatalog
	tools           map[string]*lettaTool
	turnToolCallIDs map[string]struct{}
	children        map[string]*lettaChild
	generation      providerkit.GenerationBuffer
	retries         map[string]*providerkit.RetryResolution
	// runtimeReady holds the open runtime_start handshake, until the response
	// settles it. Start waits on it before it returns the agent.
	runtimeReady         *runtimeWaiter
	catalogPending       *lettaCatalogPending
	modelPending         *lettaModelUpdatePending
	modePending          *lettaModePending
	permissions          map[string]*lettaPendingPermission
	permissionDeliveries map[string]*lettaPermissionDelivery
}

var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.InputSteerer = (*Agent)(nil)
)

// lettaSendWait limits how long SendInput waits for the server to accept input.
const lettaSendWait = 30 * time.Second

// errAgentStopped reports that the agent's process already ended.
var errAgentStopped = errors.New("the Letta Code server stopped")

// lettaSettings is the live configuration of the conversation.
type lettaSettings struct {
	model          string
	reasoningLevel string
	permissionMode string
}

// lettaCatalog is the model list the server reported.
type lettaCatalog struct {
	models []lettaModel
}

// lettaModel is one entry of list_models.
type lettaModel struct {
	id          string
	displayName string
}

type lettaModelUpdatePending struct {
	requestID     string
	model         string
	effort        string
	effortPresent bool
	waiter        *runtimeWaiter
}

// lettaTool is one open tool call.
type lettaTool struct {
	id             string
	name           string
	spanID         string
	runID          string
	subagentID     string
	executionEnded bool
}

// lettaControlKind is the `type` of a published control request. It tells a
// permission request from a question.
type lettaControlKind string

const (
	lettaControlPermission lettaControlKind = "permission"
	lettaControlAskUser    lettaControlKind = "ask_user"
)

// SendInput delivers a user message to the conversation.
//
// It returns once the input frame is written, and never waits for the turn. A
// running turn refuses the input with ErrAgentBusy, so the LeapMux input queue
// holds it; Letta itself would queue it and run it as a new turn after the
// current one, which LeapMux must show as its own turn.
//
// It never sends before the runtime identity exists. Start waits for the
// runtime_start response, and sendInput refuses an empty scope as a backstop:
// the App Server drops a scope-less input without an error.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments)
}

// SendInputForSession delivers a user message when the conversation it states
// is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments)
}

// HandleOutput feeds one stdout line into the dispatcher. The App Server
// speaks over its WebSocket; stdout carries the ready line and logs.
func (a *Agent) HandleOutput(content []byte) {
	a.handleFrame(content)
}

// SteerInput is unsupported: Letta queues a mid-turn message as a new turn.
func (a *Agent) SteerInput(string, []*leapmuxv1.Attachment) error {
	return agent.ErrSteeringUnsupported
}

// SupportsSteering reports false. Letta queues a mid-turn user message and runs
// it as a NEW turn after the current one ends; it cannot inject into the
// running turn.
func (a *Agent) SupportsSteering() bool { return false }

// sendInput writes one input command.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	payload, err := buildUserMessages(content, attachments)
	if err != nil {
		return err
	}

	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.conversationID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return errAgentStopped
	}
	scope := runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
	active := a.turnActive
	a.Mu.Unlock()
	if active {
		return agent.ErrAgentBusy
	}
	// A scope-less input is not a delivery the server can refuse: it is
	// dropped with no reply at all. Fail it here instead.
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the worker cannot send input before the Letta runtime identity arrives")
	}

	a.setTurnActive(true)
	cmd := a.createMessageCommand(scope, payload)
	// One line per delivered input. The scope fields name the runtime the
	// frame addresses, which is the difference between a turn and a silent
	// drop on this protocol.
	slog.Info("letta: send input",
		"agent_id", a.AgentID(),
		"request_id", cmd.RequestID,
		"runtime_agent_id", scope.AgentID,
		"runtime_conversation_id", scope.ConversationID,
		"content_len", len(content))
	if err := a.sendCommand(cmd); err != nil {
		slog.Warn("letta: input send failed", "agent_id", a.AgentID(), "error", err)
		a.setTurnActive(false)
		return err
	}
	return nil
}

// createMessageCommand returns the `input` command that delivers messages to the
// conversation of scope. Each such input offers the question tool, because the
// tool belongs to no built-in toolset (see lettaClientToolset).
func (a *Agent) createMessageCommand(scope runtimeScope, messages []any) lettaCommand {
	cmd := newLettaCommand("input", a.nextRequestID())
	cmd.Runtime = &scope
	cmd.Payload = map[string]any{
		"kind":           "create_message",
		"messages":       messages,
		"client_toolset": lettaClientToolset(),
	}
	return cmd
}

// runtime returns the ConversationRuntimeScope of this conversation.
func (a *Agent) runtime() runtimeScope {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}
}

// buildUserMessages encodes a user message as Letta's MessageCreate list. The
// message states a fresh id (see newLettaClientMessageID), so the worker knows
// the echo of the message when Letta Code queues it.
func buildUserMessages(content string, attachments []*leapmuxv1.Attachment) ([]any, error) {
	blocks := []any{}
	text := strings.TrimSpace(content)
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if err := (lettaProvider{}).ValidateAttachment(attachment); err != nil {
			return nil, err
		}
		if attachment.Kind == agent.AttachmentKindImage {
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": attachment.MIMEType,
					"data":       attachment.Data,
				},
			})
			continue
		}
		if s := strings.TrimSpace(string(attachment.Data)); s != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return []any{map[string]any{
		"role":                    "user",
		"content":                 blocks,
		lettaClientMessageIDField: newLettaClientMessageID(),
	}}, nil
}

// setTurnActive changes the turn flag and reserves its publication sequence under the same lock.
// A repeated state produces no publication.
func (a *Agent) setTurnActive(active bool) {
	a.Mu.Lock()
	if a.turnActive == active {
		a.Mu.Unlock()
		return
	}
	a.turnActive = active
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: active}, seq)
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive, Steerable: false}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(state, seq)
	return state
}

// Interrupt aborts the running turn with abort_message.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	// A new input must not reach the socket between the active check and the abort write.
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.Mu.Lock()
	active := a.turnActive
	a.Mu.Unlock()
	if !active {
		return nil
	}
	cmd := newLettaCommand("abort_message", a.nextRequestID())
	scope := a.runtime()
	cmd.Runtime = &scope
	return a.sendCommand(cmd)
}

// ClearContext starts a fresh conversation. Letta has no in-process clear that
// LeapMux can address, so this reports unsupported and the worker restarts.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// Stop ends the process.
func (a *Agent) Stop() {
	a.Process.Stop()
	a.finishOutputAfterExit(agent.MessageCompletionInterrupted)
	a.withdrawPermissions()
}

// Wait retires permissions when the native server exits without a turn-end frame.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.finishOutputAfterExit(a.ProcessExitCompletion())
	a.withdrawPermissions()
	a.Mu.Lock()
	transportErr := a.transportErr
	a.Mu.Unlock()
	return errors.Join(err, transportErr)
}

// nextRequestID issues a command correlation id.
func (a *Agent) nextRequestID() string {
	return fmt.Sprintf("leapmux-%d", a.NextTurnSeq())
}
