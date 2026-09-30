package droid

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages one `droid exec --input-format stream-jsonrpc` process and the
// one session LeapMux drives in it.
//
// The process speaks Factory's stream-jsonrpc protocol over stdio: every
// message is one NDJSON line carrying Factory's version stamps. The agent
// embeds Process for the lifecycle and routes conversation through
// droid.add_user_message, with notifications dispatched in output.go.
type Agent struct {
	providerkit.Process

	sink            agent.ProviderServices
	workingDir      string
	homeDir         string
	clock           quartz.Clock
	launchOpts      agent.Options
	launchSpec      launch.Spec
	cleanupSettings func()
	cleanupOnce     sync.Once

	// dispatchMu serializes event dispatch. The reader goroutine and
	// HandleOutput both reach handleFrame.
	dispatchMu sync.Mutex

	// --- guarded by Mu ---

	sessionID  string
	turnActive bool
	compaction *droidCompaction
	settings   droidSettings
	catalog    droidCatalog
	// controls holds every permission and question LeapMux published and
	// nothing resolved yet.
	controls map[string]*droidPendingControl
	// childTurns tracks each child session's turn flag. The key is the
	// childSessionId of child_session_available, which is also the registry
	// row key.
	childTurns map[string]bool
	// startupReply settles the native initialize or load request before Start
	// exposes this agent to a caller that can send a prompt.
	startupReply chan error

	// settingsApplyMu serializes native updates so one readback cannot confirm
	// another caller's choice. rpcMu guards the request inboxes and context read.
	settingsApplyMu       sync.Mutex
	rpcMu                 sync.Mutex
	pendingReplies        map[string]chan droidEnvelope
	pendingSettings       map[string]chan droidSettingsUpdated
	contextRefreshPending bool
	contextRefreshQueued  bool

	// dispatchMu guards these maps and every droidOutputState.
	childSpawns        map[string]droidChildSpawn
	childAgents        map[string]string
	childAnnouncements map[string]droidChildAnnouncementID
	outputStates       map[string]*droidOutputState
	tailMu             sync.Mutex
	childTails         map[string]*droidChildTail
	tailClosing        bool
	// beforeChildTailStateRead controls the replay lock order in tests only.
	beforeChildTailStateRead func()
	// beforeChildTailStart controls Stop during a deferred start in tests only.
	beforeChildTailStart func()
	// beforeChildTailRun controls the map-publish/start gap in tests only.
	beforeChildTailRun func()
	childConnMu        sync.Mutex
	childConns         map[string]*droidChildConnection
	childClosing       bool
	childStopOnce      sync.Once

	sendMu  sync.Mutex
	stopped bool
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

// errAgentStopped reports that the agent's process already ended.
var errAgentStopped = errors.New("the Factory Droid process has stopped")

// droidSettings is the live configuration of the session.
type droidSettings struct {
	model           string
	reasoningEffort string
	interactionMode string
	autonomyLevel   string
	permissionMode  string
}

// droidCatalog is the model list the session reported.
type droidCatalog struct {
	models []droidModel
}

// droidModel is one entry of the session's availableModels.
type droidModel struct {
	id          string
	displayName string
	efforts     []string
}

// droidTool is one open tool call.
type droidTool struct {
	id     string
	name   string
	spanID string
	input  json.RawMessage
}

// droidOutputState holds one native session's transcript assembly.
type droidOutputState struct {
	messageSpans map[string]string
	tools        map[string]*droidTool
	turnToolUse  int
	generation   providerkit.GenerationBuffer
}

// droidPendingControl is one published control request awaiting an answer.
type droidPendingControl struct {
	requestID string
	kind      droidControlKind
	toolUseID string
}

// droidControlKind distinguishes a permission request from a question.
type droidControlKind string

const (
	droidControlPermission droidControlKind = "permission"
	droidControlAskUser    droidControlKind = "ask_user"
)

// SendInput delivers a user message to the session.
//
// It returns once the CLI accepted the message -- the reply arrives when the
// message is queued -- and never waits for the turn. A running turn refuses the
// input with ErrAgentBusy, so the LeapMux input queue holds it.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, false)
}

// SendInputForSession delivers a user message when the session it states is
// still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, false)
}

// SupportsSteering reports true: Droid accepts end_of_turn during a running
// turn and puts that user message into the active agent loop.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, true)
}

// sendInput writes one droid.add_user_message request.
//
// A plain message during a turn would be queued by Droid itself and become the
// next turn out of LeapMux's sight, so the agent refuses a new message while a
// turn runs: the LeapMux input queue owns queueing. A steering message uses
// the same native placement while the turn is active.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	message, err := buildUserMessage(content, attachments)
	if err != nil {
		return err
	}

	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.stopped {
		a.Mu.Unlock()
		return errAgentStopped
	}
	active := a.turnActive
	compacting := a.compaction != nil
	sessionID := a.sessionID
	a.Mu.Unlock()

	if compacting {
		return agent.ErrAgentBusy
	}
	if steer {
		if !active {
			return agent.ErrNoActiveTurn
		}
	} else if active {
		return agent.ErrAgentBusy
	}

	params := addUserMessageParams{
		SessionID:      sessionID,
		Text:           message.Text,
		Images:         message.Images,
		QueuePlacement: droidQueueEndOfTurn,
	}
	if !steer {
		a.armTurn()
	}
	if err := a.request(droidMethodAddUserMessage, params); err != nil {
		if !steer {
			a.disarmTurn()
		}
		return err
	}
	return nil
}

// droidUserMessage holds the text and images of one native user message.
type droidUserMessage struct {
	Text   string
	Images []droidInputImage
}

// buildUserMessage sends text inline and images as native base64 sources.
func buildUserMessage(content string, attachments []*leapmuxv1.Attachment) (droidUserMessage, error) {
	parts := []string{}
	images := []droidInputImage{}
	if text := strings.TrimSpace(content); text != "" {
		parts = append(parts, text)
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if err := (droidProvider{}).ValidateAttachment(attachment); err != nil {
			return droidUserMessage{}, err
		}
		switch attachment.Kind {
		case agent.AttachmentKindImage:
			images = append(images, droidInputImage{
				Type: "base64", MediaType: attachment.MIMEType,
				Data: base64.StdEncoding.EncodeToString(attachment.Data),
			})
		case agent.AttachmentKindText:
			parts = append(parts, providerkit.BuildInlineTextAttachmentBlock(attachment))
		default:
			return droidUserMessage{}, fmt.Errorf("factory Droid cannot send the attachment %s", attachment.Filename)
		}
	}
	return droidUserMessage{Text: strings.Join(parts, "\n\n"), Images: images}, nil
}

// armTurn marks a turn active and publishes the flag. A repeat of the current
// state publishes nothing, so a frame that is not a turn signal moves nothing.
func (a *Agent) armTurn() {
	a.Mu.Lock()
	if a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = true
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: true}, seq)
}

// disarmTurn marks no turn active and publishes the flag. A repeat of the
// current state publishes nothing.
func (a *Agent) disarmTurn() {
	a.Mu.Lock()
	if !a.turnActive {
		a.Mu.Unlock()
		return
	}
	a.turnActive = false
	seq := a.NextTurnSeq()
	compacting := a.compaction != nil
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: compacting}, seq)
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive || a.compaction != nil, Steerable: a.turnActive && a.compaction == nil}
	a.Mu.Unlock()
	a.sink.SetTurnState(state, a.nextTurnSeq())
	return state
}

func (a *Agent) nextTurnSeq() uint64 {
	return a.NextTurnSeq()
}

// Interrupt aborts the agent's current turn with droid.interrupt_session.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	sessionID := a.sessionID
	active := a.turnActive
	a.Mu.Unlock()
	if !active || sessionID == "" {
		return nil
	}
	if err := a.request(droidMethodInterruptSession, interruptParams{SessionID: sessionID}); err != nil {
		return err
	}
	return nil
}

// ClearContext starts a fresh session on the running process. Droid has no
// in-process context clear that LeapMux can address, so this reports
// unsupported and the worker restarts.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// Stop ends the process.
func (a *Agent) Stop() {
	a.Mu.Lock()
	a.stopped = true
	a.Mu.Unlock()
	a.markChildTailsClosing()
	a.Process.Stop()
	a.stopChildConnections()
	a.stopChildTails()
	a.cleanupRuntimeSettings()
}

// Wait returns the native process result after its child processes and
// temporary settings are closed.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopChildConnections()
	a.stopChildTails()
	a.cleanupRuntimeSettings()
	return err
}

func (a *Agent) cleanupRuntimeSettings() {
	a.cleanupOnce.Do(func() {
		if a.cleanupSettings != nil {
			a.cleanupSettings()
		}
	})
}
