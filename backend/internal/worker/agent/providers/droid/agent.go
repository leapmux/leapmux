package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

	sink       agent.ProviderServices
	workingDir string
	clock      quartz.Clock

	// dispatchMu serializes event dispatch. The reader goroutine and
	// HandleOutput both reach handleFrame.
	dispatchMu sync.Mutex

	// --- guarded by Mu ---

	sessionID   string
	turnActive  bool
	settings    droidSettings
	catalog     droidCatalog
	turnToolUse int
	// messageSeq maps a Droid message id to the span the worker opened for it.
	messageSpans map[string]string
	// tools holds every tool call the agent opened and nothing closed yet.
	tools map[string]*droidTool
	// controls holds every permission and question LeapMux published and
	// nothing resolved yet.
	controls map[string]*droidPendingControl
	// generation holds the streamed assistant text until the message completes.
	generation providerkit.GenerationBuffer

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
	autonomyMode    string
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
	return a.sendInput(nil, content, attachments)
}

// SendInputForSession delivers a user message when the session it states is
// still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments)
}

// SupportsSteering reports true: droid.add_user_message with
// queuePlacement "end_of_loop" inserts into the running turn's next
// interruption point.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput adds a message to the running turn.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, droidQueueEndOfLoop)
}

// sendInput writes one droid.add_user_message request.
//
// A plain message during a turn would be queued by Droid itself and become the
// next turn out of LeapMux's sight, so the agent refuses a new message while a
// turn runs: the LeapMux input queue owns queueing. A steering message uses
// queuePlacement "end_of_loop" and needs the turn.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, queuePlacement ...string) error {
	text, err := buildUserText(content, attachments)
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
	sessionID := a.sessionID
	a.Mu.Unlock()

	steer := len(queuePlacement) > 0 && queuePlacement[0] == droidQueueEndOfLoop
	if steer {
		if !active {
			return agent.ErrNoActiveTurn
		}
	} else if active {
		return agent.ErrAgentBusy
	}

	placement := droidQueueEndOfTurn
	if steer {
		placement = droidQueueEndOfLoop
	}
	params := addUserMessageParams{
		SessionID:      sessionID,
		Text:           text,
		QueuePlacement: placement,
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

// buildUserText joins the message and its attachments into the one string
// `droid.add_user_message` takes.
func buildUserText(content string, attachments []*leapmuxv1.Attachment) (string, error) {
	parts := []string{}
	if text := strings.TrimSpace(content); text != "" {
		parts = append(parts, text)
	}
	for _, att := range attachments {
		if att == nil {
			continue
		}
		// Droid's text field cannot carry an image. The file name is the
		// reference the reader sees; the bytes stay out of the message.
		if strings.HasPrefix(att.GetMimeType(), "image/") {
			parts = append(parts, "[image: "+att.GetFilename()+"]")
			continue
		}
		if s := strings.TrimSpace(string(att.GetData())); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n"), nil
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
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: false}, seq)
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.turnActive, Steerable: a.turnActive}
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

// CompactContext asks Droid to compact the conversation.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" {
		return agent.ErrCompactionUnsupported
	}
	return a.request(droidMethodCompactSession, map[string]string{"sessionId": sessionID})
}

// OptionGroups returns the live configuration axes.
func (a *Agent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	settings := a.settings
	catalog := a.catalog
	a.Mu.Unlock()

	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, 3)
	if len(catalog.models) > 0 {
		groups = append(groups, droidModelGroup(catalog.models, settings.model))
	}
	effort := droidEffortGroup(settings.reasoningEffort, catalog.models, settings.model)
	if effort != nil {
		groups = append(groups, effort)
	}
	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Id:           agent.OptionIDPermissionMode,
		Label:        PermissionModeLabel,
		CurrentValue: settings.permissionMode,
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options:      permissionModeGroup.GetOptions(),
	})
	return groups
}

// SettingsSnapshot reports the live option values.
func (a *Agent) SettingsSnapshot() agent.SettingsApplyResult {
	groups := a.OptionGroups()
	values := map[string]string{}
	for _, g := range groups {
		if g.GetId() != "" && g.GetCurrentValue() != "" {
			values[g.GetId()] = g.GetCurrentValue()
		}
	}
	return agent.ConfirmedSettings(values)
}

// UpdateSettings applies each included non-empty option to the running agent.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" {
		return agent.RestartRequiredSettings(options)
	}

	patch := map[string]string{}
	for id, value := range options {
		if value == "" {
			continue
		}
		switch id {
		case agent.OptionIDModel:
			patch["modelId"] = value
		case agent.OptionIDEffort:
			patch["reasoningEffort"] = value
		case agent.OptionIDPermissionMode:
			patch["autonomyMode"] = droidAutonomyForMode(value)
		}
	}
	if len(patch) == 0 {
		return agent.ConfirmedSettings(nil)
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return agent.RestartRequiredSettings(options)
	}
	if err := a.request(droidMethodUpdateSessionSettings, updateSettingsParams{
		SessionID: sessionID,
		Settings:  raw,
	}); err != nil {
		return agent.RestartRequiredSettings(options)
	}
	a.Mu.Lock()
	if v, ok := patch["modelId"]; ok {
		a.settings.model = v
	}
	if v, ok := patch["reasoningEffort"]; ok {
		a.settings.reasoningEffort = v
	}
	if v, ok := patch["autonomyMode"]; ok {
		a.settings.autonomyMode = v
	}
	if mode := droidModeForAutonomy(a.settings.autonomyMode); mode != "" {
		a.settings.permissionMode = mode
	}
	a.Mu.Unlock()
	return agent.ConfirmedSettings(options)
}

// droidAutonomyForMode maps LeapMux's permission mode onto Droid's autonomy axis.
func droidAutonomyForMode(mode string) string {
	switch mode {
	case "auto-low":
		return droidAutonomyAutoLow
	case "auto-medium":
		return droidAutonomyAutoMedium
	case "auto-high":
		return droidAutonomyAutoHigh
	default:
		return droidAutonomyNormal
	}
}

// droidModeForAutonomy maps Droid's autonomy axis back onto LeapMux's mode.
func droidModeForAutonomy(autonomy string) string {
	switch autonomy {
	case droidAutonomyAutoLow:
		return "auto-low"
	case droidAutonomyAutoMedium:
		return "auto-medium"
	case droidAutonomyAutoHigh:
		return "auto-high"
	case droidAutonomyNormal:
		return "default"
	default:
		return ""
	}
}

// request sends one JSON-RPC request and waits for its response.
func (a *Agent) request(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	env := newDroidEnvelope(droidTypeRequest)
	env.ID = a.nextRequestID()
	env.Method = method
	env.Params = raw
	line, err := env.Marshal()
	if err != nil {
		return err
	}
	// The response is routed by the reader; a fire-and-forget write is the
	// delivery. Errors surface as notifications the driver logs.
	return a.WriteStdin(append(line, '\n'))
}

// nextRequestID issues a JSON-RPC request id.
func (a *Agent) nextRequestID() string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return fmt.Sprintf("leapmux-%d", a.NextTurnSeq())
}

// Stop ends the process.
func (a *Agent) Stop() {
	a.Mu.Lock()
	a.stopped = true
	a.Mu.Unlock()
	a.Process.Stop()
}
