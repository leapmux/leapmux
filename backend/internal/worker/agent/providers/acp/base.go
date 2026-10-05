package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/version"
)

// ACP JSON-RPC method name constants shared across all ACP providers.
const (
	MethodInitialize                  = "initialize"
	acpMethodSessionUpdate            = "session/update"
	acpMethodSessionRequestPermission = "session/request_permission"
	MethodSessionCancel               = "session/cancel"
	// The JSON-RPC cancel notification an agent sends for a request it withdraws,
	// under the two names the roster uses. Goose spells it `$/cancel_request`, and
	// `$/cancelRequest` is the Language Server Protocol spelling the same
	// convention comes from. Both identify one request by `params.requestId`.
	acpMethodCancelRequestSnake = "$/cancel_request"
	acpMethodCancelRequestCamel = "$/cancelRequest"
	MethodSessionNew            = "session/new"
	MethodSessionLoad           = "session/load"
	// MethodSessionResume reopens a stored session without replaying it. OpenCode,
	// Kilo, Grok Build and Qwen Code resume through it, because the replay of
	// session/load would draw the whole conversation in the transcript a second
	// time.
	MethodSessionResume   = "session/resume"
	MethodSessionPrompt   = "session/prompt"
	MethodSessionSetModel = "session/set_model"
	MethodSessionSetMode  = "session/set_mode"
	// MethodSessionSetConfigOption is the config-option setter (ACP's
	// session/set_config_option): params {sessionId, configId, value}, returning the
	// refreshed configOptions list. Used to write the mutable option groups
	// (e.g. OpenCode/Kilo "effort", Goose "thinking_effort") that have
	// no dedicated set_model/set_mode channel.
	MethodSessionSetConfigOption = "session/set_config_option"

	// ACP host terminal methods (agent → client). Advertised via
	// clientCapabilities.terminal; see terminal.go.
	acpMethodTerminalCreate      = "terminal/create"
	acpMethodTerminalOutput      = "terminal/output"
	acpMethodTerminalWaitForExit = "terminal/wait_for_exit"
	acpMethodTerminalKill        = "terminal/kill"
	acpMethodTerminalRelease     = "terminal/release"
	acpMethodFSReadTextFile      = "fs/read_text_file"
	acpMethodFSWriteTextFile     = "fs/write_text_file"
)

// ACP session update type constants.
const (
	acpUpdatePlan = "plan"
)

// ModeChannel identifies how an ACP provider maps configOptions `mode` to its secondary setting.
// The three values exclude each other, so one field cannot select two channel families.
// ModeChannelUnmapped tracks a permission mode and exposes configOptions `mode` as a separate option group.
type ModeChannel int

const (
	// ModeChannelUnmapped tracks permission mode through native modes/current_mode_update.
	// It exposes configOptions `mode` as a separate mutable option group.
	// This is the zero-value default. Fast Agent uses this channel.
	ModeChannelUnmapped ModeChannel = iota
	// ModeChannelPermissionMode uses configOptions `mode` to select permission mode.
	ModeChannelPermissionMode
	// ModeChannelPrimaryAgent uses configOptions `mode` to select the primary agent.
	// Only this value selects the primary-agent channel family.
	ModeChannelPrimaryAgent
)

// Base extends JSONRPCProcess with the fields and methods that every ACP agent
// shares. The package documentation lists the ACP providers.
type Base struct {
	// These fields describe ACP turns and do not belong to the JSON-RPC transport.
	promptActive bool
	// interruptRequested records that the reader stopped the running turn, so a
	// tool result that arrives afterwards reports the stop rather than whatever
	// status the provider put on it. Cursor and Reasonix send `failed` for a
	// cancelled command and OpenCode and Kilo send an empty `completed`, so
	// without this the row reports a failure the reader caused by stopping, or
	// reports nothing. Guarded by b.Mu. See noteACPInterruptRequested.
	interruptRequested bool
	// agentTurnActive records that the running turn is one the agent started by
	// itself (BeginAgentTurn), rather than a session/prompt that LeapMux sent.
	// promptActive is set too, so every check that asks "does a turn run?" reads
	// one flag. Guarded by b.Mu.
	agentTurnActive bool
	// agentTurnQueued records an agent turn that began before the base
	// processed the end of the prompt that ran. The prompt's end hands the busy
	// state to it (see BeginAgentTurn). queuedAgentTurnEnd holds its end frame
	// when that too arrived first. Guarded by b.Mu.
	agentTurnQueued    bool
	queuedAgentTurnEnd json.RawMessage
	publishTurnActive  func(active bool, seq uint64)
	steerMethod        string
	steerRunID         string
	// closesSessions records that the initialize response advertised
	// session/close, which a context clear sends for the session that it
	// replaced. Guarded by b.Mu.
	closesSessions bool
	// openRows holds the registry rows that this agent opened and did not close
	// yet, which a context clear closes. See session_retirement.go.
	openRows acpOpenRows

	providerkit.JSONRPCProcess
	sink agent.ProviderServices
	// hooks is what the provider changed about this base. applyHooks sets it
	// once, before the process starts, and nothing changes it after that, so
	// the base reads it without b.Mu.
	hooks Hooks
	acpTurnOutput
	acpTerminalHost
	// children routes the updates of each subagent into its own transcript. See
	// children.go.
	children acpChildren
	// availableCommands is the last command set that the ACP process
	// advertised. Goal-capable providers read their command token from it.
	//
	// PROCESS-scoped, not session-scoped, which is why ClearContext leaves it
	// alone while it clears every other per-session field. Goose
	// advertises once, inside the FIRST session/prompt, and never in a
	// session/new reply -- so clearing it on a context clear would disarm a
	// working control until the user's next message, and a stale update from a
	// replaced session can only restate what the same binary already offers.
	// Claude's hasGoalCommand is the same kind of answer for the same reason:
	// it describes the BUILD, not the session.
	availableCommands map[string]struct{}
	// subagentPrompts holds each spawn's prompt until the child transcript that
	// should open with it exists (see SubagentObservation.Prompt). Keyed by
	// the registry RowKey. Guarded by subagentPromptMu; entries are spent when
	// the child is created and dropped when the row closes, so a provider that
	// never links a child cannot grow it without limit.
	subagentPrompts providerkit.PendingPrompts
	// secondaryChannelOnce/secondaryChannelCache memoize the resolved secondary channel.
	// hooks.ModeChannel is fixed at construction (applyHooks) and the channel's field/list
	// POINTERS and closures all capture b (stable), so the resolution is invariant for the
	// agent's lifetime -- secondaryChannel() builds it once rather than rebuilding the
	// struct-of-closures on each of its ~7 per-operation callers. Resolved lazily on first use
	// (after applyHooks has set the mode channel), never copied (Base is always used by
	// pointer).
	secondaryChannelOnce  sync.Once
	secondaryChannelCache acpSecondaryChannel
	reapplySettings       func()                // called by ClearContext after session/new to re-apply model, mode, etc.
	refreshFromSession    func(json.RawMessage) // called by ClearContext after reapplySettings to sync state from the session response
	sessionID             string
	workingDir            string
	model                 string
	permissionMode        string
	currentPrimaryAgent   string
	availableModels       []*agent.ModelInfo
	// modelsFieldInfos holds the models reported through the SessionModelState
	// `models` field at the last full session response (handshake or ClearContext).
	// A runtime config_option_update carries only the configOptions `model` select,
	// so applyConfigOptionModelsLocked re-unions these to keep models-field-only
	// entries from vanishing mid-session for providers that split their catalog.
	modelsFieldInfos       []ModelInfo
	availableModes         []*leapmuxv1.AvailableOption
	availablePrimaryAgents []*leapmuxv1.AvailableOption
	// secondaryFallback is the static option list for this provider's secondary axis
	// (permission modes or primary agents), served by OptionGroups before the session
	// reports its catalog. Start sets it from the static groups of the provider; it
	// is nil for a provider with none (Reasonix).
	// Sourcing it here lets the one shared OptionGroups serve every ACP family without a
	// per-provider override -- the same fallback StaticSecondaryGroup uses at registration.
	secondaryFallback []*leapmuxv1.AvailableOption
	// options bundles the server-driven config-option bookkeeping for the selectors the model
	// and mode channels do not claim. All of it is guarded by b.Mu -- the same lock every
	// other Base field uses -- so a refresh can pair an option change with the
	// model/secondary under one critical section (see optionState). Carrying NO mutex of its
	// own is deliberate.
	options optionState
	// optionWriteMu serializes a whole multi-option write batch (applyOptionUpdates
	// / reapplyOptions / applyStartupOptions) against another batch, so two
	// concurrent batches can't interleave their session/set_config_option RPCs and validate
	// each id against a half-applied map. It is an OPERATION lock, distinct from the b.Mu
	// STATE lock, and is always acquired BEFORE b.Mu (never the reverse) to avoid a cycle.
	// It deliberately does NOT guard handleACPConfigOptionUpdate: that runs on the reader
	// goroutine that also delivers these RPCs' responses, so blocking it on a batch in
	// flight would deadlock; a server-initiated config_option_update may still fold mid-batch.
	optionWriteMu sync.Mutex
	// sessionMu serializes the session lifecycle (session/new + the sessionID swap, held
	// under the write lock by newSessionLocked) against every session/* RPC
	// (SetModelViaConfigOption, acpSetMode, setConfigOption, cancelSession, each holding the read lock for its
	// capture-sessionID-and-send via WithSessionID). Without it a write could capture the
	// pre-clear sessionID and send AFTER a concurrent ClearContext replaced the session,
	// targeting a torn-down session. It is acquired BEFORE b.Mu, and BELOW optionWriteMu in
	// the lock order (optionWriteMu -> sessionMu -> b.Mu); ClearContext releases it before
	// reapplySettings, whose RPCs re-acquire the read lock per call.
	sessionMu      sync.RWMutex
	sessionUpdates acpSessionUpdates
}

// handleACPPromptResponse drains one turn and persists its prompt response.
func (b *Base) handleACPPromptResponse(resp json.RawMessage) {
	if resp == nil {
		b.finishIncompleteACPPrompt(agent.MessageCompletionInterrupted)
		return
	}
	b.persistFinishedTurn(resp)
}

// persistFinishedTurn drains the turn that ended with frame and persists frame
// as its turn-end row. frame is the agent's own record of the end: the
// session/prompt response for a turn that LeapMux started, and the provider's
// end frame for a turn that the agent started by itself.
//
// A prompt with an agent turn waiting behind it drains only its own part (see
// BeginAgentTurn). Its text was persisted at the boundary, and the live counter
// of the text now counts the agent turn, so it stays open.
func (b *Base) persistFinishedTurn(frame json.RawMessage) {
	main := b.main()
	turn, bounded := b.drainPromptTurn()
	if !bounded {
		main.persistCompletedText(agent.AssembledMessageKindReasoning, turn.thoughtText)
		main.persistCompletedText(agent.AssembledMessageKindText, turn.assistantText)
	}
	// A tool the turn left unfinished failed -- unless the reader STOPPED the turn,
	// in which case it was cut rather than broken. A clean cancel returns a prompt
	// response rather than an error, so Cursor arrives here and not on the error
	// path: its stopped command stored `completion: error` for work the reader
	// chose to end.
	incomplete := agent.MessageCompletionError
	if b.acpInterruptRequested() {
		incomplete = agent.MessageCompletionInterrupted
	}
	main.persistIncompleteTools(turn.incompleteTools, incomplete)
	b.clearCompletedTerminals()
	numToolUses := turn.completedToolUses + len(turn.incompleteTools)

	b.persistPromptResponse(frame, numToolUses)
}

// acpTextProgressScope is the live-counter scope for one text kind. The scope is
// LeapMux's own key, so it keeps the protocol's update name rather than changing
// with the stored shape.
func acpTextProgressScope(kind agent.AssembledMessageKind) string {
	if kind == agent.AssembledMessageKindReasoning {
		return contracts.ACPUpdateAgentThoughtChunk
	}
	return contracts.ACPUpdateAgentMessageChunk
}

// finishIncompleteACPPrompt ends a prompt that reached no prompt response: the
// response was empty, or the request failed. A prompt with an agent turn
// waiting behind it closes only the tool calls that it left open, and the
// progress of the agent turn stays (see BeginAgentTurn).
func (b *Base) finishIncompleteACPPrompt(completion agent.MessageCompletion) {
	turn, bounded := b.drainPromptTurn()
	if !bounded {
		b.finishACPTurn(turn, completion)
		return
	}
	if b.IsDiscardingOutput() {
		return
	}
	b.main().persistIncompleteTools(turn.incompleteTools, completion)
	b.clearCompletedTerminals()
}

// finishAllTurnOutput ends every turn that has output: the prompt, and an agent
// turn that waits behind it. Stop and the exit of the process end both.
func (b *Base) finishAllTurnOutput(completion agent.MessageCompletion) {
	b.finishACPTurn(b.drainTurn(), completion)
}

func (b *Base) finishACPTurn(turn acpTurnSnapshot, completion agent.MessageCompletion) {
	if b.IsDiscardingOutput() {
		b.ResetCumulativeOutput()
		b.sink.ReportProgress(agent.ResetProgress())
		return
	}
	b.main().finishTurn(turn, completion)
	b.clearCompletedTerminals()
	b.sink.ReportProgress(agent.ResetProgress())
}

// MethodHandler is called for JSON-RPC methods not handled by the shared
// ACP dispatcher. Return true if the method was consumed.
type MethodHandler func(line *providerkit.ParsedLine) bool

// acpUpdateHeader is the part of one session update that the dispatcher reads
// before it decides where the update belongs.
type acpUpdateHeader struct {
	SessionUpdate string                     `json:"sessionUpdate"`
	Role          string                     `json:"role"`
	Status        string                     `json:"status"`
	Content       json.RawMessage            `json:"content"`
	Meta          map[string]json.RawMessage `json:"_meta"`
}

func (b *Base) handleACPUpdate(update json.RawMessage) {
	var header acpUpdateHeader
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp session update unmarshal header failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	// A subagent's update rides the main session under a tag of the provider's.
	// It belongs to the transcript of that subagent, so the main session reads
	// nothing from it -- not even its metadata, which states the usage and the
	// progress of the SUBAGENT. An update whose tag identifies no child transcript
	// stays in the main one, where the reader can still see it.
	if b.hooks.ChildUpdateRoute != nil {
		if rowKey := b.hooks.ChildUpdateRoute(header.SessionUpdate, header.Meta); rowKey != "" &&
			b.routeToChild(rowKey, header, update) != childRouteUnknown {
			return
		}
	}
	if b.hooks.SessionMetadataHandler != nil && b.hooks.SessionMetadataHandler(header.SessionUpdate, header.Meta, update) {
		return
	}

	// A native result envelope repeats the turn fields the prompt response already
	// carries, so the dispatcher below has nothing to do with it.
	if header.Role == contracts.ACPRoleResult {
		return
	}
	if b.main().handleUpdate(header, update) {
		return
	}

	switch header.SessionUpdate {
	case contracts.ACPUpdateUsageUpdate:
		b.handleUsageUpdate(update)
	case contracts.ACPUpdateConfigOptionUpdate:
		// Shared model channel for every ACP provider; mode handled per-provider.
		b.handleACPConfigOptionUpdate(update)
	case contracts.ACPUpdateCurrentMode:
		b.handleACPModeUpdate(update)
	case contracts.ACPUpdateUserMessageChunk:
		// No-op: user_message_chunk is history replay.
	case contracts.ACPUpdateAvailableCommandsUpdate:
		b.observeAvailableCommands(update)
	case contracts.ACPUpdateSessionInfoUpdate:
		// Session metadata: the runtime's own title and its modified time. Neither is
		// conversation, and one update arrives for every turn, so persisting it put a
		// raw-JSON row in every transcript. A provider that reads a field of its own
		// takes the sessionMetadataHandler above, which runs before this switch --
		// Goose reads its steer run identifier there.
		//
		// The title is a real answer the runtime computes, and LeapMux gives its tabs
		// their own names. Adopting it is a presentation decision, so it stays unread here.
	}
}

// ApplySubagentObservation applies one subagent observation that the provider
// read outside a tool call of the main session -- a notification that reports
// the spawn or the end of a subagent, or the progress of a workflow run.
func (b *Base) ApplySubagentObservation(obs *SubagentObservation) {
	b.main().applySubagentObservation(obs)
}

// observeAvailableCommands replaces the advertised ACP command set. A change
// can add or remove a goal command, so it republishes goal capabilities.
func (b *Base) observeAvailableCommands(update json.RawMessage) {
	var envelope struct {
		AvailableCommands json.RawMessage `json:"availableCommands"`
	}
	if err := json.Unmarshal(update, &envelope); err != nil || len(envelope.AvailableCommands) == 0 {
		return
	}
	var values []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(envelope.AvailableCommands, &values); err != nil {
		slog.Warn("acp available commands unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	b.ReplaceAvailableCommands(names)
}

// ReplaceAvailableCommands replaces the command set that the agent offers.
// The base reads the standard available_commands_update, and a provider whose
// agent lists its commands somewhere else as well -- Grok Build states them in
// its initialize response, before any update -- reports that list here, so
// the goal control does not wait for the first prompt. A change can add or
// remove a goal command, so it republishes the goal capabilities.
func (b *Base) ReplaceAvailableCommands(names []string) {
	commands := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			commands[name] = struct{}{}
		}
	}
	b.Mu.Lock()
	changed := !maps.Equal(b.availableCommands, commands)
	b.availableCommands = commands
	b.Mu.Unlock()
	if changed && b.sink != nil {
		b.sink.PublishGoalCapabilities()
	}
}

func (b *Base) HasAvailableCommand(command string) bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	_, ok := b.availableCommands[command]
	return ok
}

// ClearContext replaces the current session with a fresh one on the running
// ACP process, through session/new.
//
// The outgoing session must do nothing afterwards that LeapMux does not show
// (see session_retirement.go). So before session/new, the clear releases the
// outgoing session: it answers each control request that the session holds
// open, and it sends session/cancel when a turn runs. After the swap, it closes
// the rows of the outgoing session and ends that session with the agent. The
// reapplySettings callback (if set) then applies the provider settings, such as
// the model and the permission mode, to the new session.
//
// A session/new that fails leaves the outgoing session current, with its turn
// stopped, as a stop leaves it.
func (b *Base) ClearContext() (string, error) {
	b.sessionMu.Lock()
	outgoingSessionID := b.CurrentSessionID()
	if outgoingSessionID != "" {
		if err := b.releaseOutgoingSession(outgoingSessionID); err != nil {
			slog.Warn("acp cancel the turn of the outgoing session", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		}
	}
	// Host terminals belong to the outgoing session. Release the current set
	// before session/new. The session swap releases any set created during it.
	b.releaseSessionTerminals()

	sessionID, resp, outgoingTurn, err := b.newSessionLocked()
	b.sessionMu.Unlock()
	if err != nil {
		return "", err
	}
	b.notePromptActive()
	b.finishACPTurn(outgoingTurn, agent.MessageCompletionInterrupted)
	// Same for an unspent spawn prompt. Its row belongs to the OUTGOING session
	// and will never produce a closing observation to drop it, so without this
	// it is held for the life of the agent process -- and if the new session
	// ever reuses the tool-call id, it would open the next transcript on the
	// previous session's instruction. Codex clears its equivalent map here too.
	b.subagentPrompts.Clear()
	// A provider that keys its own state by tool-call id has the same exposure,
	// for the same reason, so it clears that state here.
	if b.hooks.ClearProviderState != nil {
		b.hooks.ClearProviderState()
	}
	// An agent that answered session/new with the outgoing id serves that session
	// still, so nothing of it is retired.
	if outgoingSessionID != "" && outgoingSessionID != sessionID {
		b.retireSession(outgoingSessionID)
	}

	// A goal belongs to a SESSION, and this call replaced the session. Codex and
	// ZCode clear it in their own ClearContext for the same reason; this is the
	// ACP half, and it covers every provider on this base -- Reasonix is the one
	// that reports a goal today.
	//
	// A provider that never reports a goal pays nothing: clearGoal reads the row
	// first and returns before the broadcast when there was no goal to remove.
	b.sink.ClearGoal(false)

	b.sink.UpdateSessionID(sessionID)

	// Release sessionMu before reapplySettings.
	// Its setters acquire sessionMu for reading through WithSessionID.
	if b.reapplySettings != nil {
		b.reapplySettings()
	}
	if b.refreshFromSession != nil {
		b.refreshFromSession(resp)
	}
	// The session response precedes these notifications. Replay them after the refresh.
	b.finishSessionUpdates()
	return sessionID, nil
}

// newSessionLocked sends session/new and swaps the current session.
// The caller holds sessionMu for writing until this function returns.
// Success leaves new-session updates buffered for ClearContext.
// Failure resumes dispatch for the unchanged session.
func (b *Base) newSessionLocked() (sessionID string, resp json.RawMessage, outgoing acpTurnSnapshot, err error) {
	b.beginSessionUpdates()
	defer func() {
		if err != nil {
			b.finishSessionUpdates()
		}
	}()
	_, params := buildACPSessionRequest("", b.currentWorkingDir(), MethodSessionNew, "", b.hooks.SessionParams)
	resp, err = b.SendRequest(MethodSessionNew, json.RawMessage(params), b.APITimeout())
	if err != nil {
		return "", nil, acpTurnSnapshot{}, err
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &session); err != nil {
		return "", nil, acpTurnSnapshot{}, fmt.Errorf("read new ACP session: %w", err)
	}
	if session.SessionID == "" {
		return "", nil, acpTurnSnapshot{}, fmt.Errorf("the new ACP session has no ID")
	}

	// Serialize received updates with the session swap. Preserve the previous session's final updates before its turn drains.
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	b.flushPreviousSessionUpdates()
	// Lock order: sessionMu -> sessionUpdates.mu -> lifecycleMu -> turnMu -> b.Mu.
	b.replaceTerminalSession(func() {
		outgoing = b.replaceSession(func() {
			b.Mu.Lock()
			b.sessionID = session.SessionID
			// The swap ends the turn the previous session carried, so every field
			// that belongs to that turn goes with it. The interrupt note is one of
			// them: left behind, it stamped Interrupted on every completed tool row
			// of the NEXT turn, because ClearContext reaches here without passing
			// clearActivePrompt.
			b.resetTurnStateLocked()
			// Every option payload after this point is newer than resp, which the
			// ClearContext refresh reads last. See applySessionRefresh.
			b.options.markSessionStart()
			b.Mu.Unlock()
		})
	})
	return session.SessionID, resp, outgoing, nil
}

// WithSessionID runs fn with the current session ID and holds sessionMu.RLock for the complete call.
// ClearContext holds sessionMu.Lock around session/new and the sessionID replacement in newSessionLocked.
// Thus a concurrent context clear cannot replace the session during this request.
// Each session/* RPC uses this method and targets either the previous session or its replacement.
func (b *Base) WithSessionID(fn func(sessionID string) error) error {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.Mu.Lock()
	sessionID := b.sessionID
	b.Mu.Unlock()
	return fn(sessionID)
}

// secondaryAxis holds the fixed presentation for permission mode or the primary agent.
// Each axis declares its option ID, label, and order once.
// Permission mode uses "Mode". The primary agent uses "Primary Agent".
// secondaryChannel and StaticSecondaryGroup share that declaration, so their presentation cannot differ.
type secondaryAxis struct {
	optionID string
	label    string
	order    int32
}

var (
	permissionModeAxis = secondaryAxis{optionID: agent.OptionIDPermissionMode, label: "Mode", order: agent.OptionOrderPermissionMode}
	primaryAgentAxis   = secondaryAxis{optionID: agent.OptionIDPrimaryAgent, label: "Primary Agent", order: agent.OptionOrderPrimaryAgent}
)

// secondaryAxisFor maps a mode channel to its fixed presentation axis.
func secondaryAxisFor(modeChannel ModeChannel) secondaryAxis {
	if modeChannel == ModeChannelPrimaryAgent {
		return primaryAgentAxis
	}
	return permissionModeAxis
}

// secondaryGroup combines the fixed axis presentation with its options and current value.
// A nonempty current value selects the live option. An empty value supplies the static fallback.
// Protobuf omits CurrentValue when that value is empty.
// DefaultValue uses the provider default or first option and remains independent of the live selection.
// The default badge must stay on the default option when the user selects another option.
// secondaryOptionGroupLocked and StaticSecondaryGroup share this builder and therefore produce the same fields.
func secondaryGroup(axis secondaryAxis, options []*leapmuxv1.AvailableOption, current string) *leapmuxv1.AvailableOptionGroup {
	return &leapmuxv1.AvailableOptionGroup{
		Id:           axis.optionID,
		Label:        axis.label,
		Options:      options,
		CurrentValue: current,
		DefaultValue: defaultOrFirstOption(options),
		Mutable:      true,
		Order:        axis.order,
	}
}

// StaticSecondaryGroup supplies one static permission-mode or primary-agent group before the native catalog arrives.
// Each provider supplies only its mode channel and fallback options.
// secondaryAxisFor gives the same presentation that secondaryOptionGroupLocked uses for the live group.
// The live OptionGroups path derives its fallback options from this same registration.
// The fixed order keeps the fallback after the model and effort groups.
// The provider default or first option gets the default badge before the handshake completes.
func StaticSecondaryGroup(modeChannel ModeChannel, options []*leapmuxv1.AvailableOption) []*leapmuxv1.AvailableOptionGroup {
	return []*leapmuxv1.AvailableOptionGroup{secondaryGroup(secondaryAxisFor(modeChannel), options, "")}
}

// SecondaryFallbackFrom returns the options from the group that StaticSecondaryGroup creates.
// Start derives the runtime fallback from this registration, so each provider declares its fallback list once.
// secondaryGroup preserves the original option slice.
// The unmapped channel uses the permission-mode fallback.
// A registration without that group returns nil.
func SecondaryFallbackFrom(optionGroups []*leapmuxv1.AvailableOptionGroup, modeChannel ModeChannel) []*leapmuxv1.AvailableOption {
	g := optionids.GroupByID(optionGroups, secondaryAxisFor(modeChannel).optionID)
	return g.GetOptions()
}

// acpSecondaryChannel combines the axis presentation with its runtime state and operations.
// secondaryChannel derives that data once, so settings writes and refreshes use the same field.
type acpSecondaryChannel struct {
	secondaryAxis
	// modeChannel identifies the resolved channel family.
	// Consumers use routesAsPermissionMode or routesAsPrimaryAgent without reading b.hooks.ModeChannel again.
	modeChannel ModeChannel
	field       *string
	set         func(string) error
	logKey      string
	// available points at b.availableModes or b.availablePrimaryAgents.
	// Reads and refreshes share that field pointer and therefore see each reassigned option slice.
	// Hold b.Mu when dereferencing this pointer.
	available *[]*leapmuxv1.AvailableOption
	// rebuild replaces *available from native modes and keeps the prior list when the new list is empty.
	// The caller holds b.Mu.
	rebuild func(modes []ModeInfo, reported string)
	// hiddenFilter returns "" for a hidden primary agent that the picker must not select.
	// It preserves other values. Permission mode has no hidden-value filter.
	hiddenFilter func(reported string) string
	// syncConfigOverride applies the configOptions override and reports its resolved value and changed fields.
	// It reports whether the current value and available options changed.
	// Runtime updates use this operation without selecting another family-specific method.
	// It remains nil for the unmapped channel, which has no configOptions override.
	syncConfigOverride func(configOptions []ConfigOption) (value string, changed, listChanged bool)
	// persistShape selects the stored representation of the secondary value.
	// A primary agent uses primaryAgentOptions. Permission mode uses PersistSettingsRefresh's mode argument.
	persistShape func(value string) (optionsBase map[string]string, persistMode string)
}

// secondaryChannel resolves the complete runtime channel from b.hooks.ModeChannel once.
// The result holds its presentation, state pointers, and refresh operations.
// Settings writes and refreshes use that result, so this method owns the family selection.
// The operations capture b. Hold b.Mu when an operation accesses its fields.
// secondaryChannelOnce caches the result because construction fixes the mode channel for the process lifetime.
func (b *Base) secondaryChannel() acpSecondaryChannel {
	b.secondaryChannelOnce.Do(func() { b.secondaryChannelCache = b.buildSecondaryChannel() })
	return b.secondaryChannelCache
}

func (b *Base) buildSecondaryChannel() acpSecondaryChannel {
	sc := acpSecondaryChannel{secondaryAxis: secondaryAxisFor(b.hooks.ModeChannel), modeChannel: b.hooks.ModeChannel}
	if b.hooks.ModeChannel == ModeChannelPrimaryAgent {
		sc.field, sc.set, sc.logKey = &b.currentPrimaryAgent, b.setSecondary, "primaryAgent"
		sc.available = &b.availablePrimaryAgents
		sc.rebuild = func(modes []ModeInfo, reported string) {
			if rebuilt := b.buildPrimaryAgentOptions(modes, reported); len(rebuilt) > 0 {
				b.availablePrimaryAgents = rebuilt
			}
		}
		sc.hiddenFilter = func(reported string) string {
			if b.hooks.PrimaryAgentHiddenFilter != nil && b.hooks.PrimaryAgentHiddenFilter(reported) {
				return ""
			}
			return reported
		}
		sc.syncConfigOverride = b.syncConfigOptionPrimaryAgentLocked
		sc.persistShape = func(value string) (map[string]string, string) {
			return primaryAgentOptions(value), ""
		}
	} else {
		// Permission-mode and unmapped channels share the permission-mode field and setter, as secondaryAxisFor requires.
		// The unmapped channel reads its permission mode from native modes.
		sc.field, sc.set, sc.logKey = &b.permissionMode, b.setSecondary, "permissionMode"
		sc.available = &b.availableModes
		sc.rebuild = func(modes []ModeInfo, reported string) {
			if rebuilt := buildACPModes(modes, reported, nil); len(rebuilt) > 0 {
				OrderModesPreferredFirst(rebuilt, b.hooks.PreferredFirstMode)
				b.availableModes = rebuilt
			}
		}
		sc.hiddenFilter = func(reported string) string { return reported }
		// Only the permission-mode channel consumes the configOptions override.
		// The unmapped channel leaves this operation nil.
		if b.hooks.ModeChannel == ModeChannelPermissionMode {
			sc.syncConfigOverride = b.syncConfigOptionModeLocked
		}
		sc.persistShape = func(value string) (map[string]string, string) {
			return nil, value
		}
	}
	return sc
}

// routesAsPermissionMode identifies the permission-mode family from the resolved channel.
func (sc acpSecondaryChannel) routesAsPermissionMode() bool {
	return sc.modeChannel == ModeChannelPermissionMode
}

// routesAsPrimaryAgent reports whether this secondary channel is the primary-agent family.
func (sc acpSecondaryChannel) routesAsPrimaryAgent() bool {
	return sc.modeChannel == ModeChannelPrimaryAgent
}

// effectiveSetModel selects the provider's model writer when an override exists.
// Otherwise it uses setModel. Each override owns its native model-ID conversion.
func (b *Base) effectiveSetModel() func(string) error {
	if b.hooks.ModelSetter != nil {
		return b.hooks.ModelSetter
	}
	return b.setModel
}

// effectiveSetMode selects the provider's secondary-axis writer when an override exists.
// Otherwise it uses setSecondaryViaSetMode, which validates the available options before acpSetMode.
// Each override owns its validation and must acknowledge its accepted value before returning.
func (b *Base) effectiveSetMode() func(string, func(string)) error {
	if b.hooks.ModeSetter != nil {
		return b.hooks.ModeSetter
	}
	return b.setSecondaryViaSetMode
}

// setSecondaryViaSetMode writes the secondary axis with session/set_mode, refusing
// a value the current option list does not offer.
func (b *Base) setSecondaryViaSetMode(value string, acknowledged func(string)) error {
	sc := b.secondaryChannel()
	b.Mu.Lock()
	available := *sc.available
	b.Mu.Unlock()
	return b.acpSetMode(value, available, acknowledged)
}

// reapplyModelAndSecondary restores the model and secondary setting after session/new, then restores config options.
// It uses the provider's model writer and resolved secondary channel for every ACP family.
func (b *Base) reapplyModelAndSecondary() {
	sc := b.secondaryChannel()
	b.Mu.Lock()
	model, sec := b.model, *sc.field
	// Capture the stored selections before restoring the model.
	// That write folds the new session's defaults into b.options.values.
	// raiseEffortOffNone can also change "none" to "high".
	// A later snapshot would replace the user's stored effort with those defaults.
	// This snapshot preserves the stored selections through a context clear.
	storedOptions := maps.Clone(b.options.values)
	b.Mu.Unlock()
	acpApplySetting(b.ProviderName(), b.AgentID(), "model", model, b.effectiveSetModel())
	acpApplySetting(b.ProviderName(), b.AgentID(), sc.logKey, sec, sc.set)
	b.reapplyOptions(storedOptions)
}

// setSecondary stores the native acknowledgment on the reader. It performs no
// later write, so a newer native event remains authoritative after the setter returns.
// The same callback updates the permission-mode and primary-agent axes.
func (b *Base) setSecondary(value string) error {
	sc := b.secondaryChannel()
	sessionID := b.CurrentSessionID()
	accepting := true
	seen := false
	confirmed := false
	acknowledge := func(actual string) {
		b.Mu.Lock()
		defer b.Mu.Unlock()
		if !accepting || seen {
			return
		}
		seen = true
		if actual != "" && b.sessionID == sessionID {
			*sc.field = actual
			confirmed = true
		}
	}
	err := b.effectiveSetMode()(value, acknowledge)
	b.Mu.Lock()
	accepting = false
	accepted := confirmed
	b.Mu.Unlock()
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("the native mode reply did not confirm the current session")
	}
	return nil
}

// UpdateSettings applies the model, secondary setting, and mutable config options for every ACP family.
// It uses the resolved secondary channel and effectiveSetModel.
// b.hooks.ModelIDNormalizer supplies each provider's model-ID conversion.
func (b *Base) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	sc := b.secondaryChannel()
	model := options[agent.OptionIDModel]
	if b.hooks.ModelIDNormalizer != nil {
		model = b.hooks.ModelIDNormalizer(model)
	}
	secondary := options[sc.optionID]

	// The service supplies the complete merged options map for each change.
	// Write the model and secondary values only when they differ from the current selections.
	// Otherwise an effort change would also send redundant model and mode writes.
	// applyOptionUpdates applies the same guard to config options. An unchanged value counts as success.
	b.Mu.Lock()
	curModel, curSecondary := b.model, *sc.field
	// Capture the structure generation to detect a catalog change during these writes.
	// The reader can replace b.options.groups under b.Mu between two snapshots of that slice.
	// Comparing those snapshots could report the reader's change as ours or omit our change after a reader update.
	// The monotonic generation detects either change and permits an idempotent broadcast of the current catalog.
	structureGenBefore := b.options.structureGen
	b.Mu.Unlock()

	ok := true
	if model != "" && model != curModel {
		ok = acpApplySetting(b.ProviderName(), b.AgentID(), "model", model, b.effectiveSetModel()) && ok
	}
	if secondary != "" && secondary != curSecondary {
		ok = acpApplySetting(b.ProviderName(), b.AgentID(), sc.logKey, secondary, sc.set) && ok
	}
	ok = b.applyOptionUpdates(options) && ok
	ok = b.applyLocalOptions(options) && ok

	// A model change can add or remove option groups or change their available effort values.
	// The frontend refreshes this catalog only from statusChange events.
	// A setting reply alone sends no such event, and some servers omit config_option_update notifications.
	// Publish a status refresh here when the catalog changes.
	// The context-clear and handshake paths publish their own refreshes, so the model writer does not broadcast status.
	b.Mu.Lock()
	optionGroupsChanged := b.options.structureGen != structureGenBefore
	sessionID := b.sessionID
	b.Mu.Unlock()
	// Live agents always have b.sink. Tests can create Base without a sink.
	// Keep the nil guard so a catalog change in such a test cannot panic.
	if optionGroupsChanged && b.sink != nil {
		b.sink.BroadcastStatusActive(sessionID)
	}
	if !ok {
		return agent.RestartRequiredSettings(options)
	}
	return b.SettingsSnapshot()
}

func (b *Base) SettingsSnapshot() agent.SettingsApplyResult {
	result := agent.ConfirmedSettings(agent.CurrentOptions(b.OptionGroups()))
	b.Mu.Lock()
	unresolved := b.options.unresolved.keys()
	b.Mu.Unlock()
	for _, id := range unresolved {
		result.Settlements[id] = agent.OptionSettlement{State: agent.OptionSettlementUnresolved}
	}
	return result
}

// applySessionRefresh parses the session response once and refreshes both native model channels.
// It normalizes b.model through b.hooks.ModelIDNormalizer and refreshes the resolved secondary channel.
// Both catalogs update, so a context clear can change their available options.
// reconcileCurrentOptionID resolves the selection against each refreshed list.
// The resolved secondary channel supplies its stored representation, so callers need no family-specific parameters.
func (b *Base) applySessionRefresh(resp json.RawMessage) {
	sc := b.secondaryChannel()
	// Derive the available-model list (the union of both channels) and the current
	// model/secondary id from a single parse of resp.
	var model, secondaryVal string
	var models []*agent.ModelInfo
	var modelsFieldInfos []ModelInfo
	var modes []ModeInfo
	var configOptions []ConfigOption
	if session, err := parseACPSessionResult(resp); err == nil {
		modelsFieldInfos = session.Models
		modes = session.Modes
		configOptions = session.ConfigOptions
		// Read the current model from the native models field.
		// A config-only response supplies "", which preserves the model that reapplySettings just restored.
		// The catalog still combines both model channels.
		model = session.CurrentModelID
		secondaryVal = session.CurrentModeID
		if infos, current := acpHandshakeModelInfos(session); len(infos) > 0 {
			models, _ = b.buildModels(infos, current)
		}
	} else {
		// A malformed response would otherwise preserve the stored settings without reporting the parse failure.
		// Log that failure so a broken ClearContext reply remains visible.
		slog.Warn("acp agent session refresh: failed to parse session response, keeping stored settings",
			"provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
	}
	// Keep every refresh step under one lock.
	// A concurrent config_option_update cannot then store a new option beside an old model or secondary value.
	b.Mu.Lock()
	b.refreshModelsLocked(models, modelsFieldInfos, model, b.hooks.ModelIDNormalizer)
	secondaryListBefore := *sc.available
	sc.refreshLocked(modes, secondaryVal, configOptions)
	secondaryListChanged := !protoSliceEqual(secondaryListBefore, *sc.available)
	// Refresh the mutable option groups beside the mapped channels.
	// A nonempty configOptions snapshot removes options that the new session no longer reports.
	// An empty snapshot leaves the stored options unchanged because the inventory remains unresolved.
	// The KeepingStored variant preserves the user's values that reapplyOptions just restored.
	// This captured response predates those writes and can still contain server defaults.
	//
	// A settings write that returns a newer option snapshot replaces this captured snapshot completely.
	// Skip the older snapshot, which could remove options that the write revealed.
	// For example, Kiro reports effort after the model write and omits it from session/new.
	// Compare the catalog with its state before the session change because the restoration writes do not broadcast status.
	var optionListChanged bool
	if b.options.foldedSinceSessionStart() {
		optionListChanged = b.options.groupsChangedSinceSessionStart()
	} else {
		_, optionListChanged = b.applyOptionGroupsKeepingStoredLocked(configOptions)
	}
	snapshotModel, snapshotSecondary, persistMode, optionValues := b.snapshotRefreshForPersistLocked(sc)
	sessionID := b.sessionID
	b.Mu.Unlock()
	slog.Info("acp agent settings refreshed from session",
		"provider", b.ProviderName(),
		"agent_id", b.AgentID(),
		"model", snapshotModel,
		sc.logKey, snapshotSecondary,
	)
	b.sink.PersistSettingsRefresh(acpRefreshMap(snapshotModel, persistMode, optionValues))
	// A catalog-only change leaves PersistSettingsRefresh inactive because that method merges only selected values.
	// Publish status directly so the frontend receives the new catalog, as handleACPConfigOptionUpdate does for a catalog-only change.
	// A selected-value change also makes PersistSettingsRefresh publish the current catalog.
	// Context clears normally preserve the restored values, so duplicate refreshes remain uncommon.
	if optionListChanged {
		b.sink.BroadcastStatusActive(sessionID)
	}
	// The new session can offer a different mode list, which a provider can
	// derive its goal actions from. See handleACPConfigOptionUpdate.
	if secondaryListChanged {
		b.sink.PublishGoalCapabilities()
	}
}

// refreshModelsLocked updates both model catalogs and the current model from a parsed response.
// An empty catalog preserves both prior lists together.
// Thus a later config_option_update cannot discard entries from only one model channel.
// An empty current model preserves the value that reapplySettings just restored.
// The caller holds b.Mu.
func (b *Base) refreshModelsLocked(models []*agent.ModelInfo, modelsFieldInfos []ModelInfo, model string, normalizeModel func(string) string) {
	if len(models) > 0 {
		b.availableModels = models
		b.modelsFieldInfos = modelsFieldInfos
	}
	if model != "" {
		if normalizeModel != nil {
			model = normalizeModel(model)
		}
		b.model = model
	}
}

// refreshLocked rebuilds the secondary options from native modes, as the handshake does.
// It selects a valid reported value, then a valid stored value, then the default or first option.
// An empty rebuild preserves the prior list.
// A configOptions `mode` override takes precedence when present.
// The caller holds the owning Base.Mu because these operations access Base fields.
func (sc acpSecondaryChannel) refreshLocked(modes []ModeInfo, reportedSecondary string, configOptions []ConfigOption) {
	sc.rebuild(modes, reportedSecondary)
	reportedSecondary = sc.hiddenFilter(reportedSecondary)
	if resolved := reconcileCurrentOptionID(*sc.available, reportedSecondary, *sc.field); resolved != "" {
		*sc.field = resolved
	}
	// Apply the configOptions override last, as applyHandshakeMode does.
	// sc.field points at b.permissionMode or b.currentPrimaryAgent, so the snapshot includes that override.
	// The unmapped channel has no override.
	if sc.syncConfigOverride != nil {
		sc.syncConfigOverride(configOptions)
	}
}

// snapshotRefreshForPersistLocked captures every selected value under the refresh lock.
// Thus a concurrent config_option_update cannot pair a new option with an old model or secondary value.
// A primary agent uses primaryAgentOptions as the base for the merged options.
// Permission mode uses PersistSettingsRefresh's mode argument.
// The caller holds b.Mu.
func (b *Base) snapshotRefreshForPersistLocked(sc acpSecondaryChannel) (snapshotModel, snapshotSecondary, persistMode string, optionValues map[string]string) {
	snapshotModel = b.model
	snapshotSecondary = *sc.field
	optionsBase, persistMode := sc.persistShape(snapshotSecondary)
	optionValues = b.options.mergeOptionValues(optionsBase)
	return snapshotModel, snapshotSecondary, persistMode, optionValues
}

// primaryAgentOptions supplies the selected primary agent in the options map.
// An empty agent returns nil, which tells PersistSettingsRefresh to preserve the stored values.
// A non-nil map with an empty primaryAgent would serialize to "{}" because marshalOptions omits empty values.
// That empty map would remove the stored primary agent.
// Each path that stores or reports primary-agent options uses this helper.
func primaryAgentOptions(a string) map[string]string {
	if a == "" {
		return nil
	}
	return map[string]string{agent.OptionIDPrimaryAgent: a}
}

// configurePrimaryAgents installs the handshake's primary-agent list and selection.
// It replays buffered updates before it applies the requested primary agent.
// The requested value must differ from the live selection and remain available.
func (b *Base) configurePrimaryAgents(modes []ModeInfo, currentModeID, requestedPrimaryAgent string, fallback []*leapmuxv1.AvailableOption, defaultAgent string) error {
	reported := b.buildPrimaryAgentOptions(modes, currentModeID)
	available := reported
	current := currentModeID
	if len(reported) == 0 {
		available = fallback
		if current == "" {
			current = defaultAgent
		}
	}
	// Select a visible option, as the runtime and ClearContext paths do.
	// No stored selection exists at handshake, so the resolver receives an empty prior value.
	current = reconcileCurrentOptionID(available, current, "")

	b.Mu.Lock()
	// Delay the fallback list until replay finishes. Only provider records can establish support for a requested change.
	b.availablePrimaryAgents = reported
	b.currentPrimaryAgent = current
	b.Mu.Unlock()
	b.finishSessionUpdates()
	b.Mu.Lock()
	hasACPModeList := len(b.availablePrimaryAgents) > 0
	if !hasACPModeList {
		b.availablePrimaryAgents = fallback
	}
	current = b.currentPrimaryAgent
	available = b.availablePrimaryAgents
	b.Mu.Unlock()

	if hasACPModeList && requestedPrimaryAgent != "" && requestedPrimaryAgent != current && HasOption(available, requestedPrimaryAgent) {
		if err := b.setSecondary(requestedPrimaryAgent); err != nil {
			return err
		}
	}

	return nil
}

// buildPrimaryAgentOptions converts the handshake modes channel into primary-agent
// options, normalizing names (OpenCode-family agents often report name == id or
// whitespace-only names) and skipping ids the provider marks hidden via
// primaryAgentHiddenFilter (OpenCode's compaction/title/summary pseudo-agents).
func (b *Base) buildPrimaryAgentOptions(modes []ModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	normalized := make([]ModeInfo, len(modes))
	copy(normalized, modes)
	for i := range normalized {
		normalized[i].Name = normalizeOptionName(normalized[i].Name, normalized[i].ID)
	}
	return buildACPModes(normalized, currentModeID, b.hooks.PrimaryAgentHiddenFilter)
}

// defaultOrFirstOption returns the first non-empty option id, else "". Used by
// reconcileCurrentOptionID to seed a secondary channel's current selection
// (permission mode or primary agent) when the server reports no valid current.
// ACP options carry no per-option default badge (the group's current value is
// the authoritative selection), so "first" is the only sensible seed.
func defaultOrFirstOption(options []*leapmuxv1.AvailableOption) string {
	for _, option := range options {
		if option != nil && option.Id != "" {
			return option.Id
		}
	}
	return ""
}

// reconcileCurrentOptionID resolves a secondary channel's current selection (permission
// mode or primary agent) against a freshly built option list, so the in-memory current
// never points at a value absent from the list. A non-empty `reported` value the list
// contains is adopted; otherwise a `stored` value still in the list is kept; failing
// both, it re-seeds to the list's default-or-first option. An empty list means "unknown"
// -- the session did not re-report this channel -- not "nothing is valid", so the
// reported value (else the stored one) is trusted unchanged, mirroring acpSetMode's
// len(available)>0 guard. Shared by the handshake (configurePrimaryAgents), runtime
// (syncConfigOptionSelectLocked), and ClearContext (applySessionRefresh) paths so all
// three resolve the current the same way.
func reconcileCurrentOptionID(available []*leapmuxv1.AvailableOption, reported, stored string) string {
	if len(available) == 0 {
		if reported != "" {
			return reported
		}
		return stored
	}
	if reported != "" && HasOption(available, reported) {
		return reported
	}
	if stored != "" && HasOption(available, stored) {
		return stored
	}
	return defaultOrFirstOption(available)
}

// acpApplySetting logs a warning and returns false on failure. Skips
// empty values.
func acpApplySetting(providerName, agentID, name, value string, apply func(string) error) bool {
	if value == "" {
		return true
	}
	if err := apply(value); err != nil {
		slog.Warn("failed to apply setting", "setting", name, "provider", providerName, "agent_id", agentID, "error", err)
		return false
	}
	return true
}

// acpStandardInitParams marshals the standard ACP "initialize" params shared by
// every ACP provider: protocol version 1, the LeapMux clientInfo, and
// clientCapabilities.
//
// The hooks supply the provider's part:
//
//   - clientCapabilities.terminal is true, so an agent that honors the ACP host
//     terminal capability (Goose, Reasonix) routes its shells through
//     terminal/*. The handlers live in terminal.go and surface KindShell
//     background-task rows. It is false for a provider that runs its shell
//     commands itself (Hooks.DisableHostTerminal).
//   - clientCapabilities.fs.* is true, so an agent that edits through the
//     client (Dirac's `edit_file`) reads and writes the same file state that
//     the host sees. The handlers live in fs.go (issue #370). Both are false
//     for a provider that must use its own filesystem
//     (Hooks.DisableHostFileSystem).
//   - Hooks.ClientCapabilityMeta and Hooks.InitializeMeta supply the two
//     `_meta` objects.
func acpStandardInitParams(hooks *Hooks) (json.RawMessage, error) {
	hostFileSystem := !hooks.DisableHostFileSystem
	capabilities := map[string]any{
		"fs":          map[string]bool{"readTextFile": hostFileSystem, "writeTextFile": hostFileSystem},
		"terminal":    !hooks.DisableHostTerminal,
		"elicitation": map[string]any{"form": map[string]any{}, "url": map[string]any{}},
	}
	if len(hooks.ClientCapabilityMeta) > 0 {
		capabilities["_meta"] = hooks.ClientCapabilityMeta
	}
	params := map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"clientCapabilities": capabilities,
	}
	if len(hooks.InitializeMeta) > 0 {
		params["_meta"] = hooks.InitializeMeta
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	return encoded, nil
}

// buildACPSessionRequest builds a newSession or loadSession JSON-RPC request.
// adjust, when set, changes the params before they are encoded
// (Hooks.SessionParams).
func buildACPSessionRequest(resumeSessionID, workingDir, newMethod, resumeMethod string, adjust func(method string, params map[string]any)) (method string, params []byte) {
	p := map[string]any{
		"cwd":        workingDir,
		"mcpServers": []any{},
	}
	method = newMethod
	if resumeSessionID != "" {
		p["sessionId"] = resumeSessionID
		method = resumeMethod
	}
	if adjust != nil {
		adjust(method, p)
	}
	params, err := json.Marshal(p)
	if err != nil {
		slog.Warn("acp session request marshal failed", "error", err)
	}
	return method, params
}

// wireTurnActive points the base's turn-state hook at b.sink.
//
// Called once in Start, so a new ACP provider cannot forget it: every ACP
// provider reaches that constructor, and none of them wires this itself. The
// tests that pin the behavior call this too rather than building a hook of
// their own, so a test cannot pass against wiring the constructor does not do.
//
// The hook re-reads b.sink on every call, and takes no sink of its own.
// startACPHandshake REPLACES that field with a decorator (thinkingResetSink),
// and a hook that captured the raw sink would keep publishing past the
// decorator for the life of the process. That flag is the input queue's only
// dispatch guard, so a decorator that ever overrides SetTurnState would then
// hold every later message of every ACP provider, silently.
func (b *Base) wireTurnActive() {
	b.publishTurnActive = func(active bool, seq uint64) {
		b.Mu.Lock()
		steerable := active && b.steersLocked()
		b.Mu.Unlock()
		providerkit.PublishTurnStateTo(b.sink, agent.TurnState{Active: active, Steerable: steerable}, seq)
	}
}

// steersLocked reports whether this agent can steer a running turn: the
// handshake found an advertised steer method, or the provider steers by a
// route of its own. It is the ONE answer for SupportsSteering and for the
// steerable flag of each published turn, so the Steer control and the input
// queue cannot disagree. The caller holds b.Mu.
func (b *Base) steersLocked() bool {
	return b.steerMethod != "" || b.hooks.SteersByOwnRoute
}

// notePromptActive republishes the turn state from promptActive, the single
// source. Call it after EVERY critical section that writes promptActive.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and a missing call is the only way the two can drift.
// Never called with b.Mu held: the hook broadcasts, and a broadcast can block on
// a slow transport.
func (b *Base) notePromptActive() {
	if b.publishTurnActive == nil {
		return
	}
	b.Mu.Lock()
	active := b.promptActive
	seq := b.NextTurnSeq()
	b.Mu.Unlock()
	b.publishTurnActive(active, seq)
}

// SupportsSteering reports whether the handshake found an advertised steer
// method, or the provider steers by a route of its own
// (Hooks.SteersByOwnRoute). An ACP server declares that method in its
// initialize response, so a provider that steers through it can answer only
// after the handshake.
func (b *Base) SupportsSteering() bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.steersLocked()
}

func (b *Base) SteerAdvertised(content string, attachments []*leapmuxv1.Attachment) error {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.Mu.Lock()
	method, active, sessionID := b.steerMethod, b.promptActive, b.sessionID
	b.Mu.Unlock()
	if method == "" {
		return agent.ErrSteeringUnsupported
	}
	if !active {
		return agent.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if _, err := b.SendRequest(method, params, b.APITimeout()); err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	b.Mu.Lock()
	stillActive := b.promptActive
	b.Mu.Unlock()
	if !stillActive {
		return agent.ErrNoActiveTurn
	}
	return nil
}

// Stop clears prompt state, tears down host terminals, and terminates the
// agent process.
func (b *Base) Stop() {
	b.NoteIntentionalStop()
	b.clearActivePrompt()
	b.releaseAllTerminals()
	b.Process.Stop()
	b.finishAllTurnOutput(agent.MessageCompletionInterrupted)
	b.finishAllChildConversations()
}

// Wait blocks until the agent process exits, then tears down any host
// terminals that survived a crash/natural exit (Stop already released them
// on the intentional-stop path; releaseAllTerminals is idempotent).
func (b *Base) Wait() error {
	err := b.Process.Wait()
	if b.hooks.BeforeWaitCleanup != nil {
		b.hooks.BeforeWaitCleanup()
	}
	b.releaseAllTerminals()
	b.finishAllTurnOutput(b.ProcessExitCompletion())
	b.finishAllChildConversations()
	return err
}

// stopAndWait stops the agent process and blocks until it exits. Used by the
// handshake and every Start* path to tear down a half-initialized agent on a
// fatal startup error.
func (b *Base) stopAndWait() {
	b.Stop()
	_ = b.Wait()
}

// noteACPInterruptRequested records a stop against the turn that is running.
//
// A stop that reaches an idle agent notes nothing: there is no turn to cut, and a
// note left behind would relabel the next turn's first result as interrupted.
func (b *Base) noteACPInterruptRequested() {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.promptActive {
		b.interruptRequested = true
	}
}

// acpInterruptRequested reports whether the running turn was stopped.
//
// It PEEKS rather than takes, because one stop cuts every tool still in flight and
// each of them persists its own row. The note is dropped when the turn ends.
func (b *Base) acpInterruptRequested() bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.interruptRequested
}

// clearActivePrompt resets the provider-local active-turn state.
func (b *Base) clearActivePrompt() {
	b.Mu.Lock()
	b.resetTurnStateLocked()
	b.Mu.Unlock()
	b.notePromptActive()
}

// resetTurnStateLocked drops every field that belongs to ONE turn. The caller must
// hold b.Mu. It is the single spelling of that set, so a session swap and a turn
// end cannot clear different halves of it -- which is the bug it exists to make
// impossible: the session swap cleared two of the three and left the interrupt
// note behind.
//
// It does NOT publish. clearActivePrompt calls notePromptActive after it releases
// b.Mu, and the session swap publishes once it releases the session locks, because
// notePromptActive broadcasts and a broadcast must not run under either lock.
//
// The note must die with its turn rather than be cleared at the next turn's START:
// handleToolCallUpdate reads it with no promptActive gate, and the prompt response
// runs on its own goroutine, so a tool update that trails the turn's end would
// otherwise be stamped interrupted for as long as the tab stays idle.
func (b *Base) resetTurnStateLocked() {
	b.promptActive = false
	b.agentTurnActive = false
	b.agentTurnQueued = false
	b.queuedAgentTurnEnd = nil
	b.steerRunID = ""
	b.interruptRequested = false
}

// extractACPChunkText pulls the `text` field from an ACP content envelope.
// Returns "" when the field is absent, empty, or the unmarshal fails (a warning
// is logged in the failure case). The `kind` argument labels the warning so
// failures can be attributed to a specific session update type.
func (b *Base) extractACPChunkText(content json.RawMessage, kind string) string {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &c); err != nil {
		slog.Warn("acp content unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "kind", kind, "error", err)
		return ""
	}
	return c.Text
}

// persistPromptResponse stores the turn-end row. The caller closes each text
// segment first, so this handles the response frame and the spans alone.
func (b *Base) persistPromptResponse(resp json.RawMessage, numToolUses int) {
	if err := b.sink.PersistTurnEnd(agent.WithToolUseCount(agent.MessageContent{Original: resp}, numToolUses), agent.SpanInfo{}); err != nil {
		slog.Error("persist acp prompt result", "agent_id", b.AgentID(), "error", err)
	}
	b.sink.ResetSpans()
}

// ToolCallEnvelope is the parsed shape of an ACP session/notification
// tool_call. Title/RawInput/RawOutput/Meta are carried here (previously
// dropped) so provider-specific hooks can detect a subagent spawn by input
// SHAPE rather than tool-name guessing.
type ToolCallEnvelope struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Meta       json.RawMessage `json:"_meta"`
}

// ToolOutputObservation is everything ONE live update states about a running
// call's output.
//
// One value rather than two hooks, because the two facts describe one state: the
// count and the text come from the same accumulated buffer, and a reader shown one
// from before a chunk and the other from after it sees a row that contradicts itself.
type ToolOutputObservation struct {
	// Total is how many bytes the call produced.
	Total int64
	// TotalIsMinimum says Total is a floor: output was lost before this update.
	TotalIsMinimum bool
	// Tail is the text the running row draws. Empty for a provider whose output rides
	// in the update's own content, which the shared content path reports instead.
	Tail string
	// TailLost says Tail is a SUFFIX, so the row states what is missing ahead of it.
	TailLost bool
}

// ToolCallUpdateEnvelope is the parsed shape of an ACP session/notification
// tool_call_update. Meta carries provider-specific payloads like Goose's
// tool-request notifications.
type ToolCallUpdateEnvelope struct {
	ToolCallID string          `json:"toolCallId"`
	Status     string          `json:"status"`
	Content    []ToolCallBlock `json:"content"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Title      string          `json:"title"`
	Meta       json.RawMessage `json:"_meta"`
}

// acpObservationMode controls how applySubagentObservation translates an
// observation into registry sink calls.
type acpObservationMode int

const (
	// ModeUpsert (the default) upserts the registry row, optionally persists
	// a child transcript payload, then closes if CloseRow is set. Used by every
	// detector that carries descriptive fields (spawn, tool-request, progress).
	ModeUpsert acpObservationMode = iota
	// ModeCloseOnly skips the upsert and closes an existing row without first
	// creating one. Used by closing-update detectors that fire for EVERY
	// tool_call (Goose, Cursor), so a plain tool's final update does not
	// create a spurious subagent row.
	ModeCloseOnly
)

// SubagentObservation is the neutral struct a provider-specific hook
// produces: registry upsert data (kind/rowKey/title/activity/status/group), an
// optional close, and an optional child-transcript payload (childKey +
// raw bytes to persist). Shared code translates observations into sink calls,
// so provider-specific names/shapes stay out of this file.
type SubagentObservation struct {
	// Registry fields. RowKey is the provider linkage key (toolCallId / child
	// session id). Empty RowKey => no registry write.
	RowKey string
	Kind   bgtask.Kind // defaults to Subagent
	Title  string
	// TitleIsCommand marks Title as the verbatim command of a shell row, which
	// a client sets as code. Only a provider that states the command itself sets
	// it (see bgtask.Item.TitleIsCommand).
	TitleIsCommand bool
	Activity       string
	Status         bgtask.Status
	// GroupKey and GroupLabel place the row in a group, and give the label of the group. A
	// provider that runs a workflow of subagents groups the workflow row and
	// each of its agents under the id of the run.
	GroupKey   string
	GroupLabel string
	// ChildAgentKey supplies the actual native child key when the provider knows it.
	// An empty key and empty ChildSpawnSpanID leave the observation registry-only.
	ChildAgentKey string
	// ChildSpawnSpanID requests a child from a native spawn before its native key arrives.
	ChildSpawnSpanID string
	// ChildAgentSessionID supplies the provider's native session for a new child.
	ChildAgentSessionID string
	// Prompt is the instruction the subagent was spawned with, when the
	// provider's spawn payload carries one. It becomes the child transcript's
	// first message, so the tab opens on what was asked rather than on the
	// reply.
	//
	// The prompt waits by RowKey until the child exists.
	// Some providers report the prompt before the child identity.
	// Goose identifies its child from the first forwarded tool request.
	// Closing an unlinked row drops its prompt.
	Prompt string
	// CloseRow true => give a final status to the row after the upsert (Status carries
	// the final status).
	CloseRow bool
	// Spawns identifies a subagent launch request, including a request with invalid arguments.
	// It owns no span, so a long run does not move concurrent tools one column right.
	//
	// An update that identifies a launch can also set Spawns. Ordinary progress
	// and completion observations leave it false. The provider supplies this
	// fact; shared code must not infer it from the registry fields.
	Spawns bool
	// Mode selects close-only vs upsert. Defaults to ModeUpsert (zero value);
	// set ModeCloseOnly explicitly when the observation carries no descriptive
	// fields and should close an existing row without creating one.
	Mode acpObservationMode
	// RenameFrom, when set, renames the existing row from RenameFrom to RowKey
	// BEFORE the close. A provider that opens a row under one key and learns the
	// stable child id only on the final update (OpenCode opens under the
	// toolCallId, learns the session id on the final update) sets RenameFrom
	// so the single row tracks the whole lifecycle. The alternative -- upserting
	// a second row under RowKey and separately closing RenameFrom -- leaks the
	// spawn row if the close is ever missed and splits one task across two keys.
	RenameFrom string
	// ChildTranscriptPayload, when non-nil, persists to the child transcript
	// via PersistChildMessage. Goose's tool REQUESTS use this.
	ChildTranscriptPayload []byte
	// Report is the final report that the provider returned to the parent. The
	// shared translator copies it into the child transcript. The parent keeps its
	// native tool result, so the report stays visible in both conversations.
	ReportID string
	Report   agent.SubagentReport
}

func parseACPToolCallUpdate(update json.RawMessage) (map[string]json.RawMessage, ToolCallUpdateEnvelope, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(update, &fields) != nil {
		return nil, ToolCallUpdateEnvelope{}, false
	}
	tcu, ok := decodeACPToolCallUpdate(fields)
	return fields, tcu, ok
}

// decodeACPToolCallUpdate reads the part of one `tool_call_update` that shared ACP
// code acts on.
//
// Every key comes from contracts/acp-protocol.json, because ToolSupplement builds
// and matches the SAME map from the same tables. A hand-written copy here would keep
// the old word after a rename moved the generated half, and the update would then
// decode as an empty one: no status, no content, no title -- a row that stops
// changing, with no build error and no log line.
//
// `_meta` is the one key with no constant. It is the protocol's own extension slot
// rather than a field LeapMux stores, so no contract table holds it.
func decodeACPToolCallUpdate(fields map[string]json.RawMessage) (ToolCallUpdateEnvelope, bool) {
	var update ToolCallUpdateEnvelope
	if json.Unmarshal(fields[contracts.ACPSupplementIdentityToolCallID], &update.ToolCallID) != nil {
		return ToolCallUpdateEnvelope{}, false
	}
	_ = json.Unmarshal(fields[contracts.ACPSupplementIdentityStatus], &update.Status)
	_ = json.Unmarshal(fields[contracts.ACPContentBlockContent], &update.Content)
	_ = json.Unmarshal(fields[contracts.ACPSupplementRequestTitle], &update.Title)
	update.RawInput = fields[contracts.ACPSupplementRequestRawInput]
	update.RawOutput = fields[contracts.ACPSupplementRawOutput]
	update.Meta = fields["_meta"]
	return update, true
}

// ObservationIsSpawn reports whether an observation announces that this tool
// call STARTS a subagent. Each provider's detector states the fact directly, so
// shared code reads one field instead of inferring the answer from which
// registry fields happen to be filled.
//
// The earlier inference asked whether the observation upserts a RUNNING row,
// which is a different question and gave the wrong answer: Goose's
// subagent_tool_request update reports on a subagent that already runs, upserts
// a running row, and so read as a spawn -- which took the span away from the
// tool call it rode on.
//
// The RowKey guard stays: an observation that identifies no row writes nothing, so
// it must not take a span either.
func ObservationIsSpawn(obs *SubagentObservation) bool {
	return obs != nil && obs.RowKey != "" && obs.Spawns
}

// StatusIsFinal reports whether an ACP tool_call status ends the call. These
// are the three statuses FinalStatus below maps; a tool call that reaches one
// of them sends no further update.
func StatusIsFinal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled"
}

// FinalStatus maps an ACP tool_call status to the registry final
// status. Unknown values fall through to Stopped (treat as cancelled).
func FinalStatus(s string) bgtask.Status {
	switch s {
	case "completed":
		return bgtask.StatusCompleted
	case "failed":
		return bgtask.StatusFailed
	case "cancelled":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusStopped
	}
}

// ToolCallBlock is the {type, content:{type,text}} shape ACP servers ship
// for tool_call_update.content[]. The outer type is typically "content" and
// the inner content carries the renderable payload.
type ToolCallBlock struct {
	Type    string `json:"type"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ToolCallText concatenates the text from all `{type:"content"}` blocks
// whose inner content is `{type:"text", text:...}`. Returns "" when the
// payload carries no text (e.g. status-only in_progress updates, image-only
// content, or unrecognized block types).
func ToolCallText(blocks []ToolCallBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Content.Type == "text" {
			b.WriteString(block.Content.Text)
		}
	}
	return b.String()
}

func (b *Base) handleUsageUpdate(update json.RawMessage) {
	var usage struct {
		Used int64 `json:"used"`
		Size int64 `json:"size"`
		Cost struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(update, &usage); err != nil {
		slog.Warn("acp usage update unmarshal failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}

	// ACP reports one used-token total and no breakdown, so it lands on Input and
	// the other three counts stay zero.
	contextUsage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: usage.Used})
	contextUsage[contracts.ContextUsageFieldContextWindow] = usage.Size
	info := map[string]interface{}{
		contracts.SessionInfoKeyContextUsage: contextUsage,
	}
	if usage.Cost.Amount > 0 {
		info[contracts.SessionInfoKeyTotalCostUsd] = usage.Cost.Amount
	}
	b.sink.BroadcastSessionInfo(info)
}

// SessionConfig describes the session methods for a specific ACP provider.
type SessionConfig struct {
	NewMethod    string // e.g. "session/new"
	ResumeMethod string // e.g. "session/load" or "session/resume"
}

// acpDefaultSessionConfig is the standard ACP session config used by most providers.
var acpDefaultSessionConfig = SessionConfig{
	NewMethod:    MethodSessionNew,
	ResumeMethod: MethodSessionLoad,
}

// SessionResult holds the parsed result of the ACP session handshake.
type SessionResult struct {
	SessionID      string
	CurrentModelID string
	Models         []ModelInfo
	CurrentModeID  string
	Modes          []ModeInfo
	ConfigOptions  []ConfigOption
	Raw            json.RawMessage // full session response for provider-specific parsing
}

// startACPHandshake performs the common ACP startup handshake: stderr drain,
// scanner setup, initialize request, session request (new or resume),
// session ID validation, and UpdateSessionID/BroadcastStatusActive.
func (b *Base) startACPHandshake(
	stdout, stderr io.ReadCloser,
	opts agent.Options,
	initParams json.RawMessage,
	sessionCfg SessionConfig,
) (*SessionResult, error) {
	b.DrainStderr(stderr)

	// Install the progress-reset decorator once for every ACP provider.
	b.sink = agent.NewModelProgressResetSink(b.sink)
	b.beginSessionUpdates()

	scanner := agent.NewStdoutScanner(stdout)
	go b.ReadOutputLoop(scanner, b.handleOutput)

	cleanup := b.stopAndWait

	timeout := opts.EffectiveStartupTimeout()

	// 1. Send "initialize" request.
	initResp, err := b.SendRequest(MethodInitialize, initParams, timeout)
	if err != nil {
		cleanup()
		return nil, b.FormatStartupError("initialize", err)
	}
	// Write under the lock. The note at the session-ID write below gives the
	// reason.
	steerMethod := ""
	if b.hooks.AdvertisedSteerMethod != nil {
		steerMethod = b.hooks.AdvertisedSteerMethod(initResp)
	}
	if b.hooks.InitializeResponse != nil {
		b.hooks.InitializeResponse(initResp)
	}
	closesSessions := advertisesSessionClose(initResp)
	b.Mu.Lock()
	b.steerMethod = steerMethod
	b.closesSessions = closesSessions
	b.Mu.Unlock()

	// 2. Send session request (resume or new).
	sessionMethod, sessionParams := buildACPSessionRequest(opts.ResumeSessionID, opts.WorkingDir, sessionCfg.NewMethod, sessionCfg.ResumeMethod, b.hooks.SessionParams)
	sessionResp, err := b.SendRequest(sessionMethod, json.RawMessage(sessionParams), timeout)
	if err != nil {
		cleanup()
		if opts.ResumeSessionID != "" {
			return nil, b.FormatStartupError(sessionMethod, providerkit.ResumeFailedError(opts.ResumeSessionID, err))
		}
		return nil, b.FormatStartupError(sessionMethod, err)
	}
	// The reply of a resume separates its replay from live output: the history a
	// peer sends before this reply is conversation the Worker already stores, so
	// the drain at the end of startup drops it (see acpSessionUpdates).
	if opts.ResumeSessionID != "" && sessionMethod == sessionCfg.ResumeMethod {
		b.markSessionReplayCutoff()
	}

	// 3. Parse the common session fields.
	session, err := parseACPSessionResult(sessionResp)
	if err != nil {
		cleanup()
		return nil, b.FormatStartupError("session parse", err)
	}
	if session.SessionID == "" && opts.ResumeSessionID != "" && sessionMethod == sessionCfg.ResumeMethod {
		session.SessionID = opts.ResumeSessionID
	}
	if session.SessionID == "" {
		cleanup()
		return nil, b.FormatStartupError(sessionMethod, fmt.Errorf("response did not contain a session ID"))
	}

	// The reader starts before initialization completes.
	// Buffer session updates until startup installs the session identity and settings.
	// Protect shared protocol fields with b.Mu.
	b.Mu.Lock()
	b.sessionID = session.SessionID
	b.workingDir = opts.WorkingDir
	sessionID := b.sessionID
	b.Mu.Unlock()
	b.sink.UpdateSessionID(sessionID)
	b.sink.BroadcastStatusActive(sessionID)

	return session, nil
}

func ParseAdvertisedMethod(initializeResponse []byte, namespace, expectedMethod string) string {
	var response struct {
		AgentCapabilities struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(initializeResponse, &response) != nil {
		return ""
	}
	var capability struct {
		SessionSteer struct {
			Method string `json:"method"`
		} `json:"sessionSteer"`
	}
	if json.Unmarshal(response.AgentCapabilities.Meta[namespace], &capability) == nil && capability.SessionSteer.Method == expectedMethod {
		return expectedMethod
	}
	return ""
}

// StartSpec configures Start for one ACP provider. Start runs the
// fixed launch + handshake pipeline shared by every ACP agent; the spec
// supplies only what differs between providers.
type StartSpec[T any] struct {
	Registration   agent.Registration                            // launch and option metadata of the provider
	ProviderName   string                                        // process/log name, e.g. "cursor"
	BaseArgs       []string                                      // args after the binary, e.g. {"acp"}; a provider whose args depend on the launch options builds them at the call site (see reasonix.Start)
	RCMarkerEnvKey string                                        // provider rc marker stripped + re-added on a login shell (e.g. "KILO_CLIENT"); "" if none
	PinnedEnv      []string                                      // "KEY=value" assignments that REPLACE any inherited value and any value that the user's profile exports (see PinEnv and launch.WrapSpec.SetEnv); nil for none
	SessionConfig  SessionConfig                                 // zero value -> acpDefaultSessionConfig
	NewAgent       func() *T                                     // construct a zero-value concrete agent
	Base           func(*T) *Base                                // accessor for the agent's embedded Base
	Configure      func(a *T, sink agent.ProviderServices) Hooks // the hooks of the provider; the start applies them before the process starts
	AfterHandshake func(*T, *SessionResult, agent.Options) error // post-handshake apply step; nil for none
}

// Start launches an ACP agent subprocess and performs the initialize and
// session handshake, which every ACP provider shares. The providers differ
// only in these StartSpec fields:
//
//   - The binary and its arguments.
//   - An optional rc marker.
//   - The session config.
//   - The hooks that Configure returns.
//   - The step that applies the settings after the handshake.
func Start[T any](ctx context.Context, opts agent.Options, sink agent.ProviderServices, spec StartSpec[T]) (_ agent.Agent, retErr error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, spec.Registration)
	if err != nil {
		cancel()
		return nil, err
	}
	wrap := launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launchSpec,
		BaseArgs:   spec.BaseArgs,
		WorkingDir: opts.WorkingDir,
		// The login shell runs the profile after the worker hands it cmd.Env, so a
		// pin on cmd.Env alone loses to an `export` in the profile. The shell
		// wrapper states the pins again after the profile.
		SetEnv: spec.PinnedEnv,
	}
	if spec.RCMarkerEnvKey != "" {
		wrap.StripEnvKeys = []string{spec.RCMarkerEnvKey}
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, wrap)

	// A provider rc marker is stripped from the inherited env and re-added only for
	// a login shell, so the child detects "launched by leapmux" without inheriting a
	// stale parent value. The filter is WIDER than the assignment, which is why this
	// composes FilterEnv with the pin below rather than routing wholly through PinEnv.
	env := cmd.Environ()
	if spec.RCMarkerEnvKey != "" {
		env = envutil.FilterEnv(env, spec.RCMarkerEnvKey)
		if opts.LoginShell {
			env = append(env, spec.RCMarkerEnvKey+"=1")
		}
	}
	// A pinned value REPLACES whatever the environment already holds, because the
	// reason to pin one is that LeapMux needs that exact value: an inherited
	// `OPENCODE_ENABLE_QUESTION_TOOL=0` would otherwise turn off the tool the
	// question bridge exists to answer. The same pins ride on the shell wrapper's
	// SetEnv (see above), so the user's profile cannot replace them either.
	if len(spec.PinnedEnv) > 0 {
		env = envutil.PinEnv(env, spec.PinnedEnv...)
	}
	cmd.Env = providerkit.FinalizeAgentEnv(env, opts)

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	stdout, stderrPipe := pipes.Stdout(), pipes.Stderr()

	a := spec.NewAgent()
	b := spec.Base(a)
	// providerkit.NewProcess returns a fresh value (copylocks-exempt: the RHS is a call),
	// so assigning it to the embedded Process doesn't copy a held lock.
	b.Process = providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: spec.ProviderName, ShutdownGrace: spec.Registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel)
	b.sink = sink
	b.bind(b)
	b.wireTurnActive()
	b.model = opts.Model()
	// Default settings-lifecycle hooks shared by every ACP provider: reapply on
	// relaunch/ClearContext and refresh from a session response both derive the
	// secondary axis + model writer from hooks.ModeChannel/hooks.ModelSetter, so one
	// body serves every family. The ClearContext path still checks both for nil,
	// because an agent that a test builds without Start has neither.
	b.reapplySettings = b.reapplyModelAndSecondary
	b.refreshFromSession = b.applySessionRefresh
	if spec.Configure != nil {
		b.applyHooks(spec.Configure(a, sink))
	}
	// Seed the secondary-axis fallback from the static groups of the provider, after
	// applyHooks set the mode channel. The provider registers the same groups, so it
	// states its fallback list once. A provider with no static groups (Reasonix) has
	// no fallback, and this leaves it nil.
	b.secondaryFallback = SecondaryFallbackFrom(spec.Registration.OptionGroups, b.hooks.ModeChannel)

	if err := b.StartCmd(); err != nil {
		return nil, err
	}
	// The subprocess is running now. Any failure past this point must tear it
	// down -- cancel kills the ctx-bound child -- because Start returns no
	// Agent on error, so the caller never gets a handle to Stop() it.
	defer func() {
		if retErr != nil {
			cancel()
		}
	}()

	initParams, err := acpStandardInitParams(&b.hooks)
	if err != nil {
		return nil, err
	}
	sessionCfg := spec.SessionConfig
	if sessionCfg.NewMethod == "" {
		sessionCfg = acpDefaultSessionConfig
	}
	handshake, err := b.startACPHandshake(stdout, stderrPipe, opts, initParams, sessionCfg)
	if err != nil {
		return nil, err
	}

	if spec.AfterHandshake != nil {
		if err := spec.AfterHandshake(a, handshake, opts); err != nil {
			return nil, err
		}
	}
	b.finishSessionUpdates()
	// Every concrete ACP agent (*T) implements Agent via its embedded Base
	// plus its own overrides; assert it here so Start can stay generic over T.
	agent, ok := any(a).(agent.Agent)
	if !ok {
		return nil, fmt.Errorf("acp agent %T does not implement Agent", a)
	}
	return agent, nil
}

// OptionGroups returns one ACP provider's configuration axes as option groups:
// the model group, then -- for a provider with a secondary axis (permission mode or
// primary agent) -- that mapped group carrying its current value, then any mutable option
// groups the server surfaced. The secondary group is omitted when it has neither options
// nor a current value to report. One body serves every ACP family; the axis specifics come
// from secondaryChannel and the per-provider secondaryFallback, so no provider overrides
// this.
func (b *Base) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.Mu.Lock()
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := agent.ModelOptionGroup(b.availableModels, b.model, agent.EffortSubGroups); mg != nil {
		groups = append(groups, mg)
	}
	if grp := b.secondaryOptionGroupLocked(); grp != nil {
		groups = append(groups, grp)
	}
	groups = append(groups, b.options.groups...)
	b.Mu.Unlock()
	// Outside b.Mu: the provider keeps these groups under a lock of its own.
	if b.hooks.LocalOptionGroups != nil {
		groups = append(groups, b.hooks.LocalOptionGroups()...)
	}
	return groups
}

// applyLocalOptions writes each value of a local option group that the update
// changes (Hooks.LocalOptionGroups). It returns false when a write failed.
func (b *Base) applyLocalOptions(options optionmap.Map) bool {
	if b.hooks.LocalOptionGroups == nil || b.hooks.ApplyLocalOption == nil {
		return true
	}
	ok := true
	for _, group := range b.hooks.LocalOptionGroups() {
		value, present := options[group.GetId()]
		if !present || value == "" || value == group.GetCurrentValue() {
			continue
		}
		ok = acpApplySetting(b.ProviderName(), b.AgentID(), group.GetId(), value, func(v string) error {
			handled, err := b.hooks.ApplyLocalOption(group.GetId(), v)
			if err != nil {
				return err
			}
			if !handled {
				return fmt.Errorf("the provider owns no local option %s", group.GetId())
			}
			return nil
		}) && ok
	}
	return ok
}

// secondaryOptionGroupLocked builds the mapped secondary-axis group (permission mode or
// primary agent) with its live current value, falling back to the static secondaryFallback
// list before the session reports a catalog. The unmapped channel takes the same group: it
// is the permission-mode family too, tracking the mode on the native modes channel rather
// than a configOptions `mode` select. Returns nil when the group has nothing to say -- no
// options to offer and no current value to report. Caller holds b.Mu.
func (b *Base) secondaryOptionGroupLocked() *leapmuxv1.AvailableOptionGroup {
	sc := b.secondaryChannel()
	options := *sc.available
	if len(options) == 0 {
		options = b.secondaryFallback
	}
	if len(options) == 0 && *sc.field == "" {
		return nil
	}
	return secondaryGroup(sc.secondaryAxis, options, *sc.field)
}

// parseACPSessionResult parses the model/mode/configOptions channels shared by
// every ACP session response -- the handshake (session/new, resume) and the
// ClearContext reply alike. Session-id validation is left to the caller.
func parseACPSessionResult(resp json.RawMessage) (*SessionResult, error) {
	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string      `json:"currentModelId"`
			AvailableModels []ModelInfo `json:"availableModels"`
		} `json:"models"`
		Modes *struct {
			CurrentModeID  string     `json:"currentModeId"`
			AvailableModes []ModeInfo `json:"availableModes"`
		} `json:"modes"`
		ConfigOptions []ConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(resp, &session); err != nil {
		return nil, err
	}

	result := &SessionResult{
		SessionID:      session.SessionID,
		CurrentModelID: session.Models.CurrentModelID,
		Models:         session.Models.AvailableModels,
		ConfigOptions:  session.ConfigOptions,
		Raw:            resp,
	}
	if session.Modes != nil {
		result.CurrentModeID = session.Modes.CurrentModeID
		result.Modes = session.Modes.AvailableModes
	}
	return result, nil
}

// ModeInfo is the JSON shape shared by all ACP providers for mode metadata.
type ModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModelInfo is the JSON shape shared by all ACP providers for model metadata.
// Meta is the provider's own `_meta` of the model, which Hooks.ModelDecorator
// reads.
type ModelInfo struct {
	ModelID     string          `json:"modelId"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

type ConfigOption struct {
	ID string `json:"id"`
	// Category is the ACP spec's semantic signal for a config option (reserved
	// values "model"/"mode"/"thought_level", plus custom "_"-prefixed). Unlike id
	// -- an opaque identifier the spec says "MUST NOT be required for correctness"
	// -- category is what we dispatch the model/mode channels on, falling back to
	// the well-known id. Every provider we ship today omits it (""), so the
	// id-fallback keeps parsing byte-for-byte back-compatible.
	Category string `json:"category"`
	// Type is the widget kind. The spec defines "select" only today; an empty
	// value is treated as "select" (see isSelectableConfigOption). Any other
	// (future) type is ignored defensively.
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// CurrentValue is the rendered string form of the wire value (see
	// UnmarshalJSON): a non-select widget sends its own JSON type there.
	CurrentValue string              `json:"currentValue"`
	Options      []ConfigOptionValue `json:"options"`
}

// UnmarshalJSON accepts any JSON scalar for `currentValue`. The field reads as a
// string everywhere downstream, but a server that advertises a non-select widget
// sends the value in that widget's own JSON type -- Dirac's `type: "boolean"`
// auto_approve/yolo arrive as JSON bools. isSelectableConfigOption drops those
// widgets from the option machinery, so no code surfaces the value, but the session
// parse must not die on it: the Type field already commits to ignoring unknown
// widget types defensively, and this is the value half of that same posture.
// Strings unquote, null and an absent value stay "", and any other scalar keeps its
// compact JSON literal.
func (c *ConfigOption) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID           string            `json:"id"`
		Category     string            `json:"category"`
		Type         string            `json:"type"`
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		CurrentValue json.RawMessage   `json:"currentValue"`
		Options      []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	options, err := flattenConfigOptionValues(wire.Options)
	if err != nil {
		return err
	}
	c.ID = wire.ID
	c.Category = wire.Category
	c.Type = wire.Type
	c.Name = wire.Name
	c.Description = wire.Description
	c.CurrentValue = configOptionCurrentString(wire.CurrentValue)
	c.Options = options
	return nil
}

// flattenConfigOptionValues keeps the selectable values inside ACP option
// groups. A group has an options array and no value of its own.
func flattenConfigOptionValues(raw []json.RawMessage) ([]ConfigOptionValue, error) {
	values := make([]ConfigOptionValue, 0, len(raw))
	for _, item := range raw {
		var choice struct {
			ConfigOptionValue
			Options []json.RawMessage `json:"options"`
		}
		if err := json.Unmarshal(item, &choice); err != nil {
			return nil, fmt.Errorf("decode an ACP config option value: %w", err)
		}
		if choice.Options != nil {
			children, err := flattenConfigOptionValues(choice.Options)
			if err != nil {
				return nil, err
			}
			values = append(values, children...)
			continue
		}
		values = append(values, choice.ConfigOptionValue)
	}
	return values, nil
}

// configOptionCurrentString renders a config option's raw currentValue as the
// string the option machinery stores. An absent or null value, and a JSON string,
// take their usual reading; any other scalar (a boolean or number widget's value)
// keeps its compact JSON literal rather than failing the parse.
func configOptionCurrentString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return string(raw)
	}
	return compact.String()
}

type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Meta is the provider `_meta` of the value. The model select carries the
	// metadata of each model here, which reaches Hooks.ModelDecorator.
	Meta json.RawMessage `json:"_meta,omitempty"`
}

// buildACPModels converts a list of ModelInfo into proto AvailableModel messages.
// If normalize is non-nil, it is applied to each model ID (and the currentModelID) before use.
//
// Models are deduped by their final (post-normalize) id, keeping the first
// occurrence. This matters because acpHandshakeModelInfos unions two channels and
// dedups by *raw* id: a normalizer that collapses two distinct wire ids to one
// (e.g. Cursor's "default[]" -> "auto") would otherwise emit the same model twice,
// and it also guards against a server repeating an id within a single channel.
func buildACPModels(models []ModelInfo, currentModelID string, normalize func(string) string) []*agent.ModelInfo {
	if normalize != nil {
		currentModelID = normalize(currentModelID)
	}
	result := make([]*agent.ModelInfo, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		id := m.ModelID
		if normalize != nil {
			id = normalize(id)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := m.Name
		if name == "" {
			name = id
		}
		result = append(result, &agent.ModelInfo{
			Id:          id,
			DisplayName: name,
			Description: m.Description,
			IsDefault:   id == currentModelID,
		})
	}
	return result
}

// ConfigOptionIDModel and ConfigOptionIDMode are the well-known ids ACP
// servers use for the model and mode entries inside a session's configOptions.
const (
	ConfigOptionIDModel = "model"
	ConfigOptionIDMode  = "mode"
)

// acpConfigOptionCategoryModel and acpConfigOptionCategoryMode are the ACP spec's
// reserved `category` values for the model and mode selectors. category is the
// semantic signal we dispatch on; the well-known id is only a back-compat fallback.
const (
	acpConfigOptionCategoryModel = "model"
	acpConfigOptionCategoryMode  = "mode"
	// acpConfigOptionCategoryThoughtLevel marks a reasoning-effort axis (OpenCode/Kilo
	// "effort", Goose "thinking_effort"); its options are reordered strongest-first.
	acpConfigOptionCategoryThoughtLevel = "thought_level"
)

// isSelectableConfigOption reports whether a config option is a value-list selector
// we can render. The ACP spec defines `type` as "select"-only today; an empty type
// is treated as "select" for back-compat (every provider we ship omits it). Any
// other (future) type is ignored defensively so an unknown widget kind never reaches
// a model/mode/option picker that only understands a list of values.
func isSelectableConfigOption(o ConfigOption) bool {
	return o.Type == "" || o.Type == "select"
}

// acpConfigOptionContentLess is a stable total order over config options, used ONLY to break a
// duplicate-id tie deterministically -- two options sharing an id is a spec violation, so this
// carries no semantic meaning; it just makes the resolution slice-order-independent. Orders by
// CurrentValue, then by the option values joined in sorted (order-independent) order.
func acpConfigOptionContentLess(a, b ConfigOption) bool {
	if a.CurrentValue != b.CurrentValue {
		return a.CurrentValue < b.CurrentValue
	}
	return acpConfigOptionValuesKey(a) < acpConfigOptionValuesKey(b)
}

// acpConfigOptionValuesKey joins an option's offered values in sorted order into a stable key, so
// two duplicate-id options are ordered the same regardless of the order the server lists either's
// values in.
func acpConfigOptionValuesKey(o ConfigOption) string {
	vals := make([]string, 0, len(o.Options))
	for _, v := range o.Options {
		vals = append(vals, v.Value)
	}
	slices.Sort(vals)
	return strings.Join(vals, "\x00")
}

// acpConfigOptionByCategory returns the selectable config option that carries a
// semantic channel, in ONE pass over the candidates a server may name it by:
// the exact well-known id, or the `category` reserved for the channel (the ACP
// spec's intended signal for a server that uses opaque ids). The well-known id
// wins even over a category match -- a category tag can over-apply, and Junie
// tags its Brave Mode setting `category: "mode"` beside the real `mode` select,
// so dispatching on the tag loses the session mode entirely. The id is the
// protocol's own name; "MUST NOT be required for correctness" means a server
// may omit it, not that its presence is advisory. Both signals on one option
// need no tie-break, and a duplicate id resolves to the content-smallest
// occurrence (acpConfigOptionContentLess) so the claimed axis can't flip
// between payloads the server lists in different orders. Returns the matched
// option and true, or a zero option and false when no selectable option carries
// either signal.
func acpConfigOptionByCategory(options []ConfigOption, category, fallbackID string) (ConfigOption, bool) {
	found := false
	var best ConfigOption
	for _, option := range options {
		if !isSelectableConfigOption(option) {
			continue
		}
		if option.ID != fallbackID && option.Category != category {
			continue
		}
		if !found || acpConfigOptionPreferred(option, best, fallbackID) {
			best, found = option, true
		}
	}
	return best, found
}

// acpConfigOptionPreferred orders two candidates of acpConfigOptionByCategory:
// the one carrying the well-known id wins outright, then the lowest id, then the
// content-smallest duplicate of one id. A strict weak ordering, so the winner
// never depends on the order the server lists them in.
func acpConfigOptionPreferred(candidate, incumbent ConfigOption, fallbackID string) bool {
	candidateKnown, incumbentKnown := candidate.ID == fallbackID, incumbent.ID == fallbackID
	if candidateKnown != incumbentKnown {
		return candidateKnown
	}
	if candidate.ID != incumbent.ID {
		return candidate.ID < incumbent.ID
	}
	return acpConfigOptionContentLess(candidate, incumbent)
}

// acpModelInfosFromConfigOption converts a `model` select config option into the
// common ModelInfo shape plus its current value, so callers can feed it
// through buildACPModels exactly like the SessionModelState `models` field.
func acpModelInfosFromConfigOption(option ConfigOption) ([]ModelInfo, string) {
	infos := make([]ModelInfo, 0, len(option.Options))
	for _, candidate := range option.Options {
		if candidate.Value == "" {
			continue
		}
		infos = append(infos, ModelInfo{
			ModelID:     candidate.Value,
			Name:        candidate.Name,
			Description: candidate.Description,
			Meta:        candidate.Meta,
		})
	}
	return infos, option.CurrentValue
}

// acpHandshakeModelInfos returns the available models and current model id from a
// session handshake. ACP servers report models through one or both of two
// channels: the SessionModelState `models` field, or a `model` select inside
// `configOptions` (OpenCode/Kilo use only the latter; others may use either).
// We union both channels -- deduping by model id, `models`-field entries first --
// so a provider that splits its catalog across the two, or reports a partial list
// in one, still surfaces every model. The `models` field's current id wins when
// present; otherwise the config option's current value is used.
func acpHandshakeModelInfos(handshake *SessionResult) ([]ModelInfo, string) {
	infos := handshake.Models
	current := handshake.CurrentModelID

	if option, ok := acpConfigOptionByCategory(handshake.ConfigOptions, acpConfigOptionCategoryModel, ConfigOptionIDModel); ok {
		optionInfos, optionCurrent := acpModelInfosFromConfigOption(option)
		infos = mergeModelInfos(infos, optionInfos)
		if current == "" {
			current = optionCurrent
		}
	}

	return infos, current
}

// mergeModelInfos returns the union of two model-info lists, deduped by raw model
// id with `primary` entries kept first (and their metadata preferred over a
// `secondary` duplicate). Used to union the SessionModelState `models` field with
// the configOptions `model` select -- at handshake, and again at runtime so a
// config_option_update does not drop models reported only through the `models`
// field. Returns `secondary` unchanged when `primary` is empty (the common
// OpenCode/Kilo case, where the `models` field is unused).
func mergeModelInfos(primary, secondary []ModelInfo) []ModelInfo {
	if len(primary) == 0 {
		return secondary
	}
	merged := append([]ModelInfo(nil), primary...)
	seen := make(map[string]bool, len(primary))
	for _, info := range primary {
		seen[info.ModelID] = true
	}
	for _, info := range secondary {
		if seen[info.ModelID] {
			continue
		}
		seen[info.ModelID] = true
		merged = append(merged, info)
	}
	return merged
}

// buildModels turns raw model infos into proto models, applying the provider
// model-id normalizer, and returns them alongside the normalized current model
// id. Centralizing this keeps the handshake and runtime model channels
// byte-for-byte identical in how they normalize.
func (b *Base) buildModels(infos []ModelInfo, currentModelID string) ([]*agent.ModelInfo, string) {
	models := buildACPModels(infos, currentModelID, b.hooks.ModelIDNormalizer)
	if b.hooks.ModelIDNormalizer != nil {
		currentModelID = b.hooks.ModelIDNormalizer(currentModelID)
	}
	if b.hooks.ModelDecorator != nil {
		// The metadata of the FIRST info of each final id, which is the info that
		// buildACPModels kept for that id.
		metadata := make(map[string]json.RawMessage, len(infos))
		for _, info := range infos {
			id := info.ModelID
			if b.hooks.ModelIDNormalizer != nil {
				id = b.hooks.ModelIDNormalizer(id)
			}
			if _, seen := metadata[id]; !seen {
				metadata[id] = info.Meta
			}
		}
		for _, m := range models {
			b.hooks.ModelDecorator(m, metadata[m.Id])
		}
	}
	return models, currentModelID
}

// applyHandshakeModels sets availableModels and the current model from a session
// handshake, merging both model channels (see acpHandshakeModelInfos) and writing
// under the lock. The lock matters: startACPHandshake starts the reader goroutine
// before Start* finishes, so a server that pushes a config_option_update right
// after session/new can call applyConfigOptionModelsLocked concurrently with this write.
//
// The current model is set to whatever the server reports -- even "" -- rather
// than preserved from the requested model option. That is deliberate: it lets
// trySetStartupModel compare the requested model against the server's actual
// current and push it via setModel when they differ (including when the server
// reports no model at all, the case for agents that accept arbitrary ids without
// advertising a list). Used by every ACP provider's handshake.
func (b *Base) applyHandshakeModels(handshake *SessionResult) {
	infos, current := acpHandshakeModelInfos(handshake)
	models, current := b.buildModels(infos, current)
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.availableModels = models
	b.model = current
	// Remember the models-field catalog so a later config_option_update can
	// re-union it (see applyConfigOptionModelsLocked).
	b.modelsFieldInfos = handshake.Models
	// Surface any config option the model/mode channels did not claim. This is the
	// one universal handshake step every ACP provider runs, so it is the single seam
	// for option surfacing -- and it is already under the lock. The handshake snapshot is
	// the authoritative initial state, so the payload's CurrentValue wins.
	b.applyOptionGroupsLocked(handshake.ConfigOptions)
}

// trySetStartupModel applies a requested model during startup when possible.
// An empty request or an unchanged model normally needs no write.
// A rejected model logs a warning and preserves the native current model without aborting startup.
// Some providers accept arbitrary IDs without a catalog, so a rejection must not discard an otherwise valid session.
// effectiveSetModel selects the provider's native model writer.
//
// Hooks.ModelWriteRevealsOptions requires a write even for an empty or unchanged request.
// Both cases write the native current model to obtain its additional options.
// If the request and current model are both empty, no write occurs.
func (b *Base) trySetStartupModel(requested string) {
	b.Mu.Lock()
	current := b.model
	b.Mu.Unlock()
	reveals := b.hooks.ModelWriteRevealsOptions
	if requested == "" && reveals {
		requested = current
	}
	if requested == "" {
		return
	}
	if requested == current && !reveals {
		return
	}
	if err := b.effectiveSetModel()(requested); err != nil {
		slog.Warn("requested model not applied; keeping current model",
			"provider", b.ProviderName(), "agent_id", b.AgentID(),
			"requested", requested, "current", current, "error", err)
	}
}

// applyStartupPermissionMode applies a requested permission mode during startup.
// An empty request or an unchanged mode needs no write.
// A rejected requested mode returns an error and aborts startup because that selection is mandatory.
//
// One case never aborts: a mode LeapMux itself chose (the new-session safe default)
// that this session's own mode list does not offer. Goose declares smart_approve as
// its safe default, and a build that reports no such mode would otherwise fail every
// new session with "unknown mode" -- a mode the user never asked for killing the tab.
// The session keeps the mode the handshake reported instead, and the warning states
// what happened. An EXPLICIT request still aborts, so a typed --permission-mode that
// this build cannot enter is reported rather than silently downgraded. Claude
// also falls back from its safe default in applyStartupPermissionMode.
//
// The current mode is read under b.Mu: startACPHandshake starts the reader
// goroutine before Start* reaches this point, and that goroutine can write
// permissionMode concurrently (syncConfigOptionModeLocked for a permission-mode provider).
// This mirrors trySetStartupModel's locked read of b.model.
func (b *Base) applyStartupPermissionMode(requested string, defaulted bool) error {
	if requested == "" {
		return nil
	}
	b.Mu.Lock()
	current := b.permissionMode
	available := b.availableModes
	b.Mu.Unlock()
	if requested == current {
		return nil
	}
	if defaulted && len(available) > 0 && !HasOption(available, requested) {
		slog.Warn("this session does not offer the safe default permission mode; keeping the mode it reported",
			"provider", b.ProviderName(), "agent_id", b.AgentID(),
			"requested", requested, "current", current)
		return nil
	}
	return b.setSecondary(requested)
}

// applyHandshakeMode sets availableModes and the permission mode from a session
// handshake (under the lock), falling back to defaultMode when the server reports none.
// A `mode` config option overrides the permission mode read from the modes channel only
// for a provider that consumes it (ModeChannelPermissionMode);
// for an unmapped provider it is left to applyOptionGroupsLocked to
// surface as a option group, matching the runtime and ClearContext paths so the option
// resolves the same way at every seam instead of being applied as the permission mode here
// but surfaced uniformly there.
// Used by ACP providers that track a permission mode (Cursor, Goose, Grok Build,
// Kiro, Qwen Code, Reasonix).
func (b *Base) applyHandshakeMode(handshake *SessionResult, defaultMode string) {
	modes := buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	OrderModesPreferredFirst(modes, b.hooks.PreferredFirstMode)
	mode := handshake.CurrentModeID
	if mode == "" {
		mode = defaultMode
	}
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.availableModes = modes
	b.permissionMode = mode
	if b.hooks.ModeChannel == ModeChannelPermissionMode {
		b.syncConfigOptionModeLocked(handshake.ConfigOptions)
	}
}

// applySecondaryStartup runs the post-handshake startup sequence shared by both ACP
// secondary families: apply the handshake models, configure the family's secondary axis
// (fatal on rejection -- the agent is torn down and a startup error returned), then push
// the requested model LAST and best-effort, and finally apply the remaining startup
// options. Keeping the order here in one body means the load-bearing invariant -- the
// model is applied last so a rejected model id cannot undo the secondary axis or abort an
// otherwise-healthy session -- can't drift between the two families. configureSecondary
// wires the family-specific secondary configuration (permission mode or primary agent),
// which is the only step that differs.
func (b *Base) applySecondaryStartup(handshake *SessionResult, opts agent.Options, requestedModel string, configureSecondary func() error) error {
	b.applyHandshakeModels(handshake)
	if err := configureSecondary(); err != nil {
		b.stopAndWait()
		return b.FormatStartupError(MethodSessionSetMode, err)
	}
	b.trySetStartupModel(requestedModel)
	b.applyStartupOptions(opts)
	return nil
}

// ApplyPermissionModeStartup runs the post-handshake startup sequence for the
// permission-mode providers (Cursor, Goose, Grok Build, Kiro, Qwen Code, Reasonix): write the mode channel from the
// handshake and push the requested permission mode. Cursor passes its normalized model
// id; trySetStartupModel routes through effectiveSetModel, which picks Cursor's
// wire-mapping setCursorModel automatically. See applySecondaryStartup for the shared
// model-last ordering.
func (b *Base) ApplyPermissionModeStartup(handshake *SessionResult, opts agent.Options, defaultMode, requestedModel string) error {
	return b.applySecondaryStartup(handshake, opts, requestedModel, func() error {
		b.applyHandshakeMode(handshake, defaultMode)
		b.finishSessionUpdates()
		return b.applyStartupPermissionMode(
			opts.PermissionMode(), opts.NewSessionDefaultOptionIDs[agent.OptionIDPermissionMode])
	})
}

// ApplyPrimaryAgentStartup runs the post-handshake startup sequence for the primary-agent
// providers (OpenCode, Kilo): configure the available primary agents and apply the
// requested one from the persisted options. The primary-agent mirror of
// ApplyPermissionModeStartup; the two differ only in this secondary configuration. The
// fallback list is read from b.secondaryFallback (which Start sets from the static groups
// of the provider before the handshake), so each provider sources its primary-agent list
// exactly once.
func (b *Base) ApplyPrimaryAgentStartup(handshake *SessionResult, opts agent.Options, defaultAgent string) error {
	return b.applySecondaryStartup(handshake, opts, opts.Model(), func() error {
		return b.configurePrimaryAgents(handshake.Modes, handshake.CurrentModeID, opts.Get(agent.OptionIDPrimaryAgent), b.secondaryFallback, defaultAgent)
	})
}

// handleACPConfigOptionUpdate processes a config_option_update notification. The
// model channel is handled uniformly for every ACP provider; the configOptions
// `mode` select is applied as the permission mode for ModeChannelPermissionMode
// providers or the primary agent for ModeChannelPrimaryAgent
// providers (OpenCode/Kilo); any unmapped option is surfaced as a mutable option
// group. All
// channels mutate under a single lock so a concurrent settings read can never observe
// a half-applied update. Broadcasts happen after the lock is released.
//
// A setting change (model, primary agent, or an option value) persists and broadcasts
// the full settings exactly once via BroadcastSettingsRefresh; when only the
// permission mode changed, UpdatePermissionMode persists+broadcasts it together with
// the chat notification. A mode change that rides alongside a model/primary-agent/
// option change emits only the chat notification, since BroadcastSettingsRefresh
// already carried the new mode in its StatusChange -- avoiding a second, transiently
// stale-model broadcast.
func (b *Base) handleACPConfigOptionUpdate(update json.RawMessage) {
	options := parseACPConfigOptions(update)
	if len(options) == 0 {
		return
	}

	b.Mu.Lock()
	b.options.clearUnresolved()
	oldMode := b.permissionMode
	modelChanged, listChanged := b.applyConfigOptionModelsLocked(options)
	// Apply the secondary axis (permission mode or primary agent) through the resolved channel,
	// so this path no longer hand-picks which family-specific Locked method to call -- that
	// distinction lives in secondaryChannel(). The returned value/changed are then mapped onto
	// the family the downstream switch routes on (mode -> Notify/UpdatePermissionMode, primary
	// agent -> full refresh).
	sc := b.secondaryChannel()
	var secondaryValue string
	var secondaryChanged, secondaryListChanged bool
	if sc.syncConfigOverride != nil {
		secondaryValue, secondaryChanged, secondaryListChanged = sc.syncConfigOverride(options)
		listChanged = listChanged || secondaryListChanged
	}
	mode := secondaryValue
	modeChanged := secondaryChanged && sc.routesAsPermissionMode()
	primaryAgentChanged := secondaryChanged && sc.routesAsPrimaryAgent()
	// Surface/sync any unmapped config option (mutable option groups). A
	// option value change persists via BroadcastSettingsRefresh (below); a
	// option list-only change rides the status-refresh branch. A server-initiated update is
	// authoritative, so the payload's CurrentValue wins.
	optionValueChanged, optionListChanged := b.applyOptionGroupsLocked(options)
	sessionID := b.sessionID
	b.Mu.Unlock()

	switch {
	case modelChanged || primaryAgentChanged || optionValueChanged:
		// A model, primary-agent, or option-value change persists+broadcasts the full
		// settings in one StatusChange (which re-fetches the live model list and carries
		// the live mode and options), so the frontend reflects the runtime switch
		// immediately. A option value must persist via BroadcastSettingsRefresh -- not
		// BroadcastStatusActive -- so the new selection survives in the options column.
		b.BroadcastSettingsRefresh()
		if modeChanged {
			// The StatusChange above already carried the new mode; emit only the chat
			// settings_changed notification rather than a second StatusChange.
			b.sink.NotifyPermissionModeChanged(oldMode, mode)
		}
	case modeChanged:
		// Mode-only change: persist the mode, broadcast the StatusChange (which carries
		// the live model list), and emit the chat notification -- all in one call.
		b.sink.UpdatePermissionMode(mode)
	case listChanged || optionListChanged:
		// An available list changed (models, modes, primary agents, or a config option
		// set) but no current selection did. BroadcastSettingsRefresh would no-op
		// (PersistSettingsRefresh skips when model/mode/options are unchanged), so
		// broadcast a status refresh directly -- its StatusChange re-fetches the live
		// model list and option groups, surfacing the new options.
		b.sink.BroadcastStatusActive(sessionID)
	}
	// A provider can derive its goal actions from the modes that the session
	// offers (Reasonix sets a goal through its `goal` mode), so a new mode list
	// can add or remove a goal action. The capability is read from the live
	// agent and broadcast only when republished, so without this the goal card
	// kept the actions of the list the session reported first.
	if secondaryListChanged {
		b.sink.PublishGoalCapabilities()
	}
}

// applyConfigOptionModelsLocked refreshes the catalog and current model from configOptions `model`.
// It applies b.hooks.ModelIDNormalizer and b.hooks.ModelDecorator through buildModels.
// It reports whether the current model and available catalog changed.
// The caller holds b.Mu. Every provider that supplies this native channel uses the same runtime update.
//
// configOptions `model` supplies only the models from that channel.
// Merge the retained modelsFieldInfos, so each update preserves models from the other channel.
func (b *Base) applyConfigOptionModelsLocked(options []ConfigOption) (modelChanged, listChanged bool) {
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	if !ok {
		return false, false
	}
	infos, current := acpModelInfosFromConfigOption(option)
	infos = mergeModelInfos(b.modelsFieldInfos, infos)
	models, current := b.buildModels(infos, current)
	// The `len(models) > 0` guard is intentional: an update that rebuilds to an empty
	// list never replaces a populated catalog. We would rather keep showing the last
	// known models than blank the picker -- an empty model list is a worse experience
	// than a momentarily stale one, and a genuinely model-less update is not a shape
	// our providers produce. Do not "fix" this to clear the list on empty.
	if len(models) > 0 && !agent.ModelInfosEqual(b.availableModels, models) {
		b.availableModels = models
		listChanged = true
	}
	if current != "" && current != b.model {
		b.model = current
		modelChanged = true
	}
	return modelChanged, listChanged
}

// protoSliceEqual reports whether two proto-message slices are identical in order and
// per-entry fields, so the model/mode/primary-agent channels can tell an actual list
// change from an idempotent re-send and avoid a redundant broadcast. proto.Equal
// compares every field, so a future field addition that a runtime update can change
// cannot silently make a real change look idempotent.
func protoSliceEqual[T proto.Message](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// buildOptionValues converts the value list of a single config-option select into
// proto AvailableOptions: deduping by value (first occurrence wins, mirroring
// buildACPModels), skipping empty and hidden-filtered ids, and normalizing the
// name the same way the handshake path (buildPrimaryAgentOptions) does so an option
// renders identically wherever it is built -- before and after a runtime
// config_option_update, and for the mode and option channels alike. The group's default
// is set by the caller at the group level (buildOptionGroup's DefaultValue), not per
// option. Shared by buildConfigOptionSelect (mode) and applyOptionGroupsLocked (option groups).
func buildOptionValues(option ConfigOption, hiddenFilter func(string) bool) []*leapmuxv1.AvailableOption {
	built := make([]*leapmuxv1.AvailableOption, 0, len(option.Options))
	seen := make(map[string]bool, len(option.Options))
	for _, candidate := range option.Options {
		if candidate.Value == "" || seen[candidate.Value] || (hiddenFilter != nil && hiddenFilter(candidate.Value)) {
			continue
		}
		seen[candidate.Value] = true
		built = append(built, &leapmuxv1.AvailableOption{
			Id:          candidate.Value,
			Name:        providerkit.TitleCaseID(candidate.Value, normalizeOptionName(candidate.Name, candidate.Value)),
			Description: candidate.Description,
		})
	}
	return built
}

// buildConfigOptionSelect converts the `mode` select of a configOptions payload
// into proto options and reports its current value, applying an optional hidden
// filter. ok is false when the payload carries no `mode` option. Shared by the
// permission-mode and primary-agent sync paths, which differ only in which field
// they store the result into.
func buildConfigOptionSelect(options []ConfigOption, hiddenFilter func(string) bool) (built []*leapmuxv1.AvailableOption, current string, ok bool) {
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, ConfigOptionIDMode)
	if !ok {
		return nil, "", false
	}
	return buildOptionValues(option, hiddenFilter), option.CurrentValue, true
}

// syncConfigOptionSelectLocked refreshes one secondary channel (the permission
// mode or the primary agent) from the `mode` select of a configOptions payload. It
// writes the available-option list and the current value into the caller-supplied
// fields, and reports the new value, whether the current value changed, and whether
// the available list changed. preferredFirst, when non-empty, lists that option id
// first before the comparison, so the rebuilt list matches what the handshake path
// produced and an unchanged catalog compares equal. The caller must hold b.Mu. Shared by
// syncConfigOptionModeLocked and syncConfigOptionPrimaryAgentLocked, which differ
// only in the hidden filter, the preferred first option, and the two target fields.
func (b *Base) syncConfigOptionSelectLocked(
	options []ConfigOption,
	hiddenFilter func(string) bool,
	preferredFirst string,
	available *[]*leapmuxv1.AvailableOption,
	currentField *string,
) (value string, changed, listChanged bool) {
	built, current, ok := buildConfigOptionSelect(options, hiddenFilter)
	if !ok {
		return "", false, false
	}
	OrderModesPreferredFirst(built, preferredFirst)
	// The len>0 guard mirrors the model channel (applyConfigOptionModelsLocked): an
	// update that rebuilds to an empty list never blanks a populated picker.
	if len(built) > 0 && !protoSliceEqual(*available, built) {
		*available = built
		listChanged = true
	}
	// Resolve the current against the (possibly rebuilt) list: adopt the server's reported
	// value when selectable, keep the stored selection if it survived the rebuild, else
	// re-seed to the default-or-first option -- so a runtime update that drops the active
	// option (or reports a hidden/absent current) never leaves the picker showing a
	// selection with no matching option. Re-seeding (vs. clearing to "") keeps the value
	// non-empty so it persists cleanly for both families -- a cleared primary agent would
	// hit primaryAgentOptions' keep-stored nil and desync memory from the DB. This is the
	// same resolution the handshake (configurePrimaryAgents) and ClearContext
	// (applySessionRefresh) paths apply.
	resolved := reconcileCurrentOptionID(*available, current, *currentField)
	changed = resolved != "" && resolved != *currentField
	if resolved != "" {
		*currentField = resolved
	}
	return resolved, changed, listChanged
}

// syncConfigOptionModeLocked refreshes permissionMode and availableModes from the
// `mode` select of a configOptions payload, returning the new mode value, whether
// it changed, and whether the available-mode list changed. The caller must hold
// b.Mu. Used by ACP providers whose configOptions `mode` maps to the permission
// mode (Cursor, Goose, Grok Build, Kiro, Qwen Code, Reasonix).
func (b *Base) syncConfigOptionModeLocked(options []ConfigOption) (string, bool, bool) {
	return b.syncConfigOptionSelectLocked(options, nil, b.hooks.PreferredFirstMode, &b.availableModes, &b.permissionMode)
}

// syncConfigOptionPrimaryAgentLocked refreshes currentPrimaryAgent and
// availablePrimaryAgents from the `mode` select of a configOptions payload,
// returning the new value, whether it changed, and whether the available list
// changed. The caller must hold b.Mu. Used by ACP providers whose configOptions
// `mode` maps to the primary agent (OpenCode, Kilo), so a server-initiated runtime
// primary-agent switch is reflected -- the mirror of syncConfigOptionModeLocked for
// the permission-mode providers.
func (b *Base) syncConfigOptionPrimaryAgentLocked(options []ConfigOption) (string, bool, bool) {
	return b.syncConfigOptionSelectLocked(options, b.hooks.PrimaryAgentHiddenFilter, "", &b.availablePrimaryAgents, &b.currentPrimaryAgent)
}

// acpRefreshMap builds the PersistSettingsRefresh delta for the ACP providers. model and mode
// are OMITTED when empty so the stored value is preserved (an ACP agent can't report a mode it
// doesn't track, or a model the server never advertised, and ACP providers never track effort).
// The option values (nil when nothing is surfaced) are overlaid verbatim, carrying cleared
// options as explicit "" entries -- the optionmap.Map merge contract then deletes them.
func acpRefreshMap(model, mode string, optionValues optionmap.Map) optionmap.Map {
	refresh := make(optionmap.Map, len(optionValues)+2)
	for k, v := range optionValues {
		refresh[k] = v
	}
	if model != "" {
		refresh[agent.OptionIDModel] = model
	}
	if mode != "" {
		refresh[agent.OptionIDPermissionMode] = mode
	}
	return refresh
}

// BroadcastSettingsRefresh persists and broadcasts the agent's current settings.
// It reads the live model/mode/primary-agent state, so it serves both permission-
// mode providers (currentPrimaryAgent == "" -> nil option values) and primary-agent
// providers (permissionMode == "" -> the stored mode is preserved by the sink).
func (b *Base) BroadcastSettingsRefresh() {
	b.Mu.Lock()
	model := b.model
	mode := b.permissionMode
	// Carry the live option values too: a model/primary-agent change that did not
	// also touch the options must still re-include them or the refresh would not
	// reflect them.
	optionValues := b.options.mergeOptionValues(primaryAgentOptions(b.currentPrimaryAgent))
	b.Mu.Unlock()
	b.sink.PersistSettingsRefresh(acpRefreshMap(model, mode, optionValues))
}

// buildACPModes converts a list of ModeInfo into proto AvailableOption messages.
// If filter is non-nil, modes for which filter returns true are skipped.
func buildACPModes(modes []ModeInfo, currentModeID string, filter func(id string) bool) []*leapmuxv1.AvailableOption {
	result := make([]*leapmuxv1.AvailableOption, 0, len(modes))
	for _, mode := range modes {
		if mode.ID == "" {
			continue
		}
		if filter != nil && filter(mode.ID) {
			continue
		}
		name := providerkit.TitleCaseID(mode.ID, mode.Name)
		result = append(result, &leapmuxv1.AvailableOption{
			Id:          mode.ID,
			Name:        name,
			Description: mode.Description,
		})
	}
	return result
}

// OrderModesPreferredFirst moves the option whose id is `preferred` to the front of
// options, and keeps every other option in the order the server reported. An empty
// `preferred`, an absent id, or an empty list leaves the slice untouched.
//
// A provider declares the id once (Base.preferredFirstMode) and every site that
// rebuilds its list calls this, so the handshake list, the live config-option list and
// the static fallback list cannot disagree about which mode comes first. That matters
// beyond the drawing order: secondaryGroup reads position 0 for the group's
// DefaultValue, and reconcileCurrentOptionID re-seeds the current selection from it.
//
// sort.SliceStable rather than a shift, matching providerkit.SortEffortsDescending: the predicate
// is a strict weak ordering with two classes (the preferred id, and everything else),
// so a server that reports the preferred id TWICE leaves both copies at the front and
// rotates nothing.
func OrderModesPreferredFirst(options []*leapmuxv1.AvailableOption, preferred string) {
	if preferred == "" {
		return
	}
	sort.SliceStable(options, func(i, j int) bool {
		return options[i].GetId() == preferred && options[j].GetId() != preferred
	})
}

func parseACPConfigOptions(raw json.RawMessage) []ConfigOption {
	var payload struct {
		ConfigOptions []ConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("acp config options unmarshal failed", "error", err)
		return nil
	}
	return payload.ConfigOptions
}

// sendSessionRPC sends an ACP session/* request through WithSessionID.
// It adds the current sessionId to extraParams and returns the decoded native result or protocol error.
// Each caller therefore shares the same session lock and JSON-RPC handling.
// A caller can use the result body to refresh options or discard it when acknowledgment suffices.
// cancelSession sends a notification without a reply and does not use this method.
func (b *Base) sendSessionRPC(method string, extraParams map[string]interface{}) (json.RawMessage, error) {
	return b.sendSessionRPCObserved(method, extraParams, nil)
}

// sendSessionRPCObserved validates the ACP object before the reader observes its reply.
// Generic JSON-RPC permits other result types, but these ACP setting methods require an object.
func (b *Base) sendSessionRPCObserved(method string, extraParams map[string]interface{}, observe func(json.RawMessage, error)) (json.RawMessage, error) {
	var out json.RawMessage
	err := b.WithSessionID(func(sessionID string) error {
		params := make(map[string]interface{}, len(extraParams)+1)
		params["sessionId"] = sessionID
		for key, value := range extraParams {
			params[key] = value
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		var objectErr error
		response, err := b.SendRequestObserved(method, raw, b.APITimeout(), func(result json.RawMessage, replyErr error) {
			if replyErr == nil {
				var object map[string]json.RawMessage
				if json.Unmarshal(result, &object) != nil || object == nil {
					objectErr = fmt.Errorf("ACP %s reply must be an object", method)
					replyErr = objectErr
				}
			}
			if observe != nil {
				observe(result, replyErr)
			}
		})
		if err != nil {
			return err
		}
		if objectErr != nil {
			return objectErr
		}
		out = response
		return nil
	})
	return out, err
}

// SetModelViaConfigOption writes configId "model" through native session/set_config_option and leaves b.model unchanged.
// Each caller stores its model ID. A provider can normalize the native ID before that write.
// Thus a concurrent OptionGroups read cannot observe or persist a temporary native ID such as "default[]".
//
// This method serves providers that support the model config-option channel.
// Its response must carry the complete refreshed configOptions snapshot.
// That snapshot can add or remove the effort group or change its choices with the model.
// The native session/set_model response supplies no such snapshot.
// Providers that lack this config-option channel supply their own model writer through hooks.ModelSetter.
func (b *Base) SetModelViaConfigOption(wireModel string) error {
	resp, err := b.sendSessionRPC(MethodSessionSetConfigOption, map[string]interface{}{
		"configId": ConfigOptionIDModel,
		"value":    wireModel,
	})
	if err != nil {
		return err
	}
	// Refresh the mutable groups from the returned configOptions snapshot.
	// applyOptionGroupsLocked skips claimed model and mode channels, so the caller still owns the model field.
	// A missing snapshot marks the known options unresolved and preserves their stored data.
	// UpdateSettings publishes a changed catalog. The context-clear path publishes its own refresh.
	options := parseACPConfigOptions(resp)
	if len(options) > 0 {
		b.Mu.Lock()
		b.options.clearUnresolved()
		b.applyOptionGroupsLocked(options)
		b.Mu.Unlock()
	} else {
		b.Mu.Lock()
		b.options.markKnownUnresolved()
		b.Mu.Unlock()
	}
	// A newly reported effort group can default to "none" and disable reasoning.
	// raiseEffortOffNone selects an actual effort after this snapshot resolves the current value.
	// Its native config-option write keeps the daemon and frontend aligned.
	b.raiseEffortOffNone(options)
	return nil
}

// SetModeViaConfigOption folds native config options and acknowledges the mode
// before the reader releases the waiter. A missing option snapshot stays unresolved.
func (b *Base) SetModeViaConfigOption(wireMode string, acknowledged func(string)) error {
	if acknowledged == nil {
		return errors.New("the mode writer requires an acknowledgment callback")
	}
	_, err := b.sendSessionRPCObserved(MethodSessionSetConfigOption, map[string]interface{}{
		"configId": ConfigOptionIDMode,
		"value":    wireMode,
	}, func(response json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		options := parseACPConfigOptions(response)
		confirmed := wireMode
		if len(options) > 0 {
			b.Mu.Lock()
			b.options.clearUnresolved()
			b.applyOptionGroupsLocked(options)
			b.Mu.Unlock()
			for _, option := range options {
				if option.ID == ConfigOptionIDMode && option.CurrentValue != "" {
					confirmed = option.CurrentValue
					break
				}
			}
		} else {
			b.Mu.Lock()
			b.options.markKnownUnresolved()
			b.Mu.Unlock()
		}
		acknowledged(confirmed)
	})
	return err
}

// setModel writes the model via session/set_config_option and updates the local field.
func (b *Base) setModel(model string) error {
	if err := b.SetModelViaConfigOption(model); err != nil {
		return err
	}
	b.Mu.Lock()
	b.model = model
	b.Mu.Unlock()
	return nil
}

// acpSetMode sends session/set_mode and acknowledges its accepted value before waiter delivery.
// A nonempty available list must contain modeID. A missing acknowledgment handler returns an error.
func (b *Base) acpSetMode(modeID string, available []*leapmuxv1.AvailableOption, acknowledged func(string)) error {
	if acknowledged == nil {
		return errors.New("the mode writer requires an acknowledgment callback")
	}
	if len(available) > 0 && !HasOption(available, modeID) {
		return fmt.Errorf("unknown mode: %s", modeID)
	}
	_, err := b.sendSessionRPCObserved(MethodSessionSetMode, map[string]interface{}{"modeId": modeID}, func(_ json.RawMessage, replyErr error) {
		if replyErr == nil {
			acknowledged(modeID)
		}
	})
	return err
}

// setConfigOption writes a mutable option through session/set_config_option and refreshes its returned snapshot.
// A mutable option has no dedicated model or mode channel.
// The native catalog must advertise configID, including an option whose current value remains unresolved.
func (b *Base) setConfigOption(configID, value string) error {
	return b.setConfigOptionGuarded(configID, value, nil)
}

// setConfigOptionGuarded is setConfigOption with an optional last-moment precondition. stillWanted
// (when non-nil) is evaluated under the SAME b.Mu acquisition as the known/offered checks -- the
// tightest point before the wire send -- so a caller whose write is only valid while the live state
// still holds (raiseEffortOffNone: "the effort axis is still at the daemon's none/off default") can
// abort if a concurrent fold already moved it. handleACPConfigOptionUpdate folds under b.Mu, so such
// a fold either lands before this read (the precondition sees it and skips) or after the send (the
// daemon's own ordering resolves the two writes); only the async RPC itself remains outside the lock,
// an irreducible window we deliberately do not close by holding b.Mu across an RPC. A false
// precondition is a no-op success.
func (b *Base) setConfigOptionGuarded(configID, value string, stillWanted func() bool) error {
	// Check the advertised-option set (every option the server has advertised) rather than
	// the surfaced-option values (only those with a concrete current value surfaced): an option
	// the server advertised with an empty current is pushable so its persisted preference
	// can be re-applied, even though it isn't yet surfaced as a group.
	b.Mu.Lock()
	known := b.options.known.has(configID)
	offered := b.options.offersValue(configID, value)
	// Evaluate the precondition under this same lock so it can't be invalidated between the check
	// and the checks below by a concurrent b.Mu holder.
	wanted := stillWanted == nil || stillWanted()
	b.Mu.Unlock()
	if !known {
		return fmt.Errorf("unknown config option: %s", configID)
	}
	if !wanted {
		// A concurrent fold moved the axis off the value this write was predicated on; skip it
		// (no-op success) rather than clobber the daemon-chosen value.
		return nil
	}
	// Skip a value the current option list does not offer rather than force-pushing it: on a
	// model switch the merged options map can still carry the PRIOR model's effort tier (e.g.
	// "xhigh") that the new model's axis dropped, and pushing it would draw a daemon rejection
	// that fails the live edit and bounces UpdateSettings into a relaunch. Treated as a no-op
	// success -- the running session keeps its actual value, and applySettingsLive's readback
	// settles the stored row to that real value. offersValue is permissive for an option with
	// no advertised list (re-pushable persisted preference), so this only drops a genuinely
	// unoffered value.
	if !offered {
		slog.Info("config option value not offered by current option list; skipping write",
			"provider", b.ProviderName(), "agent_id", b.AgentID(), "option", configID, "value", value)
		return nil
	}

	resp, err := b.sendSessionRPC(MethodSessionSetConfigOption, map[string]interface{}{
		"configId": configID,
		"value":    value,
	})
	if err != nil {
		return err
	}
	// Refresh mutable options from the authoritative returned snapshot.
	// An accepted write without that snapshot records the requested value and marks its option unresolved.
	// Thus readback does not restore the previous choice or claim that the native snapshot confirmed the requested value.
	b.Mu.Lock()
	if options := parseACPConfigOptions(resp); len(options) > 0 {
		b.options.clearUnresolved()
		b.applyOptionGroupsLocked(options)
	} else {
		b.options.markUnresolved(configID)
		b.options.recordOptimistic(configID, value, b.hooks.EffortConfigID)
	}
	b.Mu.Unlock()
	return nil
}

// withOptionWriteBatch owns the optionWriteMu->b.Mu lock ordering and the snapshot
// clone that every config-option write batch needs -- a discipline documented at
// length on optionWriteMu and otherwise easy to subtly re-implement wrong (an inverted
// lock order deadlocks; a missing clone races the reader goroutine that folds a
// server-initiated config_option_update mid-batch). optionWriteMu is held for the whole
// batch (serializing it against another batch); b.Mu is released before fn runs because
// fn's per-id RPCs re-lock it. fn receives consistent snapshots of the current option
// values and the known-option id set, and must iterate those rather than the live maps.
func (b *Base) withOptionWriteBatch(fn func(values map[string]string, known []string)) {
	b.optionWriteMu.Lock()
	defer b.optionWriteMu.Unlock()
	b.Mu.Lock()
	values := maps.Clone(b.options.values)
	known := b.options.known.keys()
	b.Mu.Unlock()
	fn(values, known)
}

// forEachOption iterates every config option -- the sorted union of the ADVERTISED ids
// (known) and the ids carrying a surfaced value (values) -- under the shared write-batch
// discipline (withOptionWriteBatch). For each id it calls decide(id, current) for the value
// to write; an empty value or want==false skips the id, otherwise the value is written via
// applyConfigOption. The "value == current" skip is deliberately the caller's choice (reapply
// re-pushes the same value, so it must NOT skip), so the driver does not bake it in. The
// per-id success aggregate is returned (callers that don't care discard it).
//
// Iterating known (not just values) matters for applyOptionUpdates: an option advertised with
// an empty current at handshake is known-but-unvalued, and a live edit targeting it must still
// reach setConfigOption rather than being silently skipped -- otherwise UpdateSettings reports
// success and the service persists/broadcasts a value the running session never applied (until
// the next relaunch's applyStartupOptions, which iterates known too). A known-but-unvalued id
// has current "" here, so reapplyOptions (which re-pushes current) skips it via the empty-value
// guard and is unaffected.
func (b *Base) forEachOption(decide func(id, current string) (value string, want bool)) bool {
	ok := true
	b.withOptionWriteBatch(func(values map[string]string, known []string) {
		for _, id := range sortedOptionIDs(known, values) {
			value, want := decide(id, values[id])
			if !want || value == "" {
				continue
			}
			ok = b.applyConfigOption(id, value) && ok
		}
	})
	return ok
}

// sortedOptionIDs returns the sorted, de-duplicated union of the advertised ids (known) and
// the ids that carry a surfaced value (values). known is normally a superset of the value
// keys, but including both covers a valued id that LRU eviction may have dropped from the
// advertised set, so no surfaced selection is missed.
func sortedOptionIDs(known []string, values map[string]string) []string {
	seen := make(map[string]struct{}, len(known)+len(values))
	ids := make([]string, 0, len(known)+len(values))
	add := func(id string) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, id := range known {
		add(id)
	}
	for id := range values {
		add(id)
	}
	slices.Sort(ids)
	return ids
}

// applyConfigOption writes one config-option value via
// session/set_config_option, logging success/failure through acpApplySetting. Shared
// by the sparse-update and ClearContext-reapply batches (forEachOption's apply
// callback) so the write wiring lives in one place.
func (b *Base) applyConfigOption(id, value string) bool {
	return b.applyConfigOptionGuarded(id, value, nil)
}

// applyConfigOptionGuarded is applyConfigOption with an optional last-moment precondition threaded
// through to setConfigOptionGuarded (see there). Used by raiseEffortOffNone so its rank-0 re-check
// runs under the write's own b.Mu acquisition rather than an earlier, wider-windowed one.
func (b *Base) applyConfigOptionGuarded(id, value string, stillWanted func() bool) bool {
	return acpApplySetting(b.ProviderName(), b.AgentID(), id, value, func(val string) error {
		return b.setConfigOptionGuarded(id, val, stillWanted)
	})
}

// applyOptionUpdates writes every mutable config-option value present
// in a sparse settings update whose value differs from the current selection, via
// session/set_config_option. Returns false if any write failed; the agent stays
// usable (a rejected option keeps its prior value), and the caller treats false as
// "not fully applied". Ids are applied in sorted order for deterministic logging.
func (b *Base) applyOptionUpdates(options map[string]string) bool {
	return b.forEachOption(func(id, current string) (string, bool) {
		v, present := options[id]
		return v, present && v != current
	})
}

// reapplyOptions re-applies the user's stored config-option selections after a session/new
// (ClearContext), mirroring reapplyModelAndSecondary for the model/mode channels, so a user's
// effort / reasoning-effort / allow-all choice survives a context clear. `stored` is the
// snapshot reapplyModelAndSecondary captured BEFORE the model re-push.
func (b *Base) reapplyOptions(stored map[string]string) {
	// Re-push the stored value unconditionally -- the server reset to its default on
	// session/new, so there is no "== current" skip here (that would skip everything). Push
	// from `stored` rather than the live current: the model write's fold (and raiseEffortOffNone)
	// overwrote b.options.values with the fresh session's defaults, so the live current is no
	// longer the user's selection. An empty stored value is skipped by forEachOption's guard.
	b.forEachOption(func(id, _ string) (string, bool) { return stored[id], true })
}

// applyStartupOptions applies a requested config-option value from the
// launch options after the handshake surfaced the server's options, best-effort
// (like trySetStartupModel): a relaunch's fresh process starts on the server default,
// so a persisted preference (e.g. a chosen reasoning effort) is re-pushed here. A
// rejected option is logged and skipped, never aborting an otherwise-healthy session.
func (b *Base) applyStartupOptions(opts agent.Options) {
	// A daemon may drive its reasoning-effort axis under a NON-"effort" id (Goose
	// thinking_effort, or a thought_level-categorized custom id), but the
	// operator env-effort override (resolveProviderDefaults / EffortEnvOverride) is stored under
	// the well-known "effort" id. Resolve the axis id once so the loop below can map the "effort"
	// override onto it -- mirroring the model/mode channels' well-known-id fallback, so the default
	// is re-pushed regardless of the daemon's id.
	effortID := b.startupEffortConfigID()

	// Iterate over every option the server has ADVERTISED (the advertised-option set), not just
	// those with a surfaced current value (the surfaced-option values): an option reported with
	// an empty current at handshake is known-but-unvalued, and its persisted preference
	// must still be re-pushed here so a fresh relaunched process leaves the server default.
	b.withOptionWriteBatch(func(values map[string]string, known []string) {
		for _, id := range slices.Sorted(slices.Values(known)) {
			requested := opts.Get(id)
			// The advertised effort axis under a non-"effort" id has no value under its own key in
			// opts; fall back to the well-known "effort" override so it is still applied.
			if requested == "" && id == effortID {
				requested = opts.Get(agent.OptionIDEffort)
			}
			if requested == "" || requested == values[id] {
				continue
			}
			if err := b.setConfigOption(id, requested); err != nil {
				slog.Warn("requested config option not applied; keeping current",
					"provider", b.ProviderName(), "agent_id", b.AgentID(),
					"option", id, "requested", requested, "current", values[id], "error", err)
			}
		}
	})
}

// acpPermissionCancelAnswer is the outcome the protocol defines for a permission
// request that the client withdraws without a reader decision. It invents no decision
// the reader did not make.
func acpPermissionCancelAnswer() any {
	return map[string]any{"outcome": map[string]any{"outcome": contracts.ACPPermissionOutcomeCancelled}}
}

// cancelSession sends a session/cancel notification for the current session.
func (b *Base) cancelSession() error {
	return b.WithSessionID(b.sendSessionCancel)
}

// Interrupt aborts the active ACP turn by sending the `session/cancel`
// notification. Every ACP server that LeapMux supports recognizes that wire
// format, and the Provider.IsInterrupt classifier expects it.
//
// Every ACP agent embeds Base, so this one implementation serves every ACP
// provider.
//
// No-op when no session is open (sessionID still empty) so
// the worker InterruptAgent RPC can be called unconditionally without
// the caller having to wait for the ACP handshake to complete.
func (b *Base) Interrupt() error {
	if b.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	b.Mu.Lock()
	sessionID := b.sessionID
	b.Mu.Unlock()
	if sessionID == "" {
		return nil
	}
	// Noted BEFORE the cancel goes out, so a result the provider sends the instant
	// it receives one is already known to belong to a stop.
	b.noteACPInterruptRequested()
	// The answers go FIRST. The agent blocks on them, so a cancel that arrives while
	// one is outstanding stops nothing until the block is released.
	// releaseOutgoingSession keeps the same order for a context clear, which
	// already holds sessionMu and so cannot cancel through WithSessionID.
	b.withdrawTurnControls()
	return b.cancelSession()
}

// HasOption returns true if any option in the slice has the given id.
func HasOption(options []*leapmuxv1.AvailableOption, id string) bool {
	if id == "" {
		return false
	}
	for _, option := range options {
		if option != nil && option.Id == id {
			return true
		}
	}
	return false
}

// handleACPCancelRequest withdraws the control request an agent cancels.
//
// This is a PROTOCOL notification and not transcript content, so it writes no
// row. Without this case the shared dispatcher's default branch persisted the
// raw frame, and a reader who stopped a turn saw a line of JSON-RPC where the
// withdrawn request had been.
//
// The withdrawal is separate from the `cancelled` outcome LeapMux sends for its
// own interrupt (see WithdrawAllControlRequests). An agent may cancel a
// request nobody asked it to cancel, which is the case only this handler covers.
//
// It sends the agent no answer. The agent withdrew the request itself, so it waits
// for nothing.
func (b *Base) handleACPCancelRequest(params json.RawMessage) {
	var notification struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return
	}
	identity, valid := agent.NewControlRequestIdentity(notification.RequestID)
	if !valid {
		return
	}
	b.WithdrawControlRequest(b.sink, identity.Key)
}

// handleOutput dispatches a single parsed output line. Used as the providerkit.LineHandler
// for ReadOutputLoop.
func (b *Base) handleOutput(line *providerkit.ParsedLine) {
	slog.Debug("acp HandleOutput", "provider", b.ProviderName(), "agent_id", b.AgentID(), "method", line.Method, "len", len(line.Raw))
	b.handleACPOutput(line)
}

// HandleOutput processes a single JSONL notification from an ACP provider.
func (b *Base) HandleOutput(content []byte) {
	b.handleOutput(providerkit.ParseLine(content))
}

// handleACPOutput is the shared output dispatcher for all ACP providers.
// It routes session updates and permission requests. It offers every other
// method to the provider's extraMethod, and persists what that leaves.
func (b *Base) handleACPOutput(line *providerkit.ParsedLine) {
	switch line.Method {
	case acpMethodSessionUpdate:
		b.handleACPSessionUpdate(line.Params)
	case contracts.MCPElicitationMethodACP:
		b.PublishSessionControlRequest(line, providerkit.MCPElicitationCancelAnswer())
	case acpMethodSessionRequestPermission:
		b.notePermissionToolCall(line.Params)
		b.PublishSessionControlRequest(line, acpPermissionCancelAnswer())
	case acpMethodCancelRequestSnake, acpMethodCancelRequestCamel:
		b.handleACPCancelRequest(line.Params)
	case acpMethodTerminalCreate,
		acpMethodTerminalOutput,
		acpMethodTerminalWaitForExit,
		acpMethodTerminalKill,
		acpMethodTerminalRelease:
		b.handleTerminalMethod(line)
	case acpMethodFSReadTextFile, acpMethodFSWriteTextFile:
		b.handleFSMethod(line)
	default:
		if b.hooks.ExtraMethod != nil && b.hooks.ExtraMethod(line) {
			return
		}
		// A request needs a response even if the transcript write fails.
		b.RefuseUnsupportedRequest(line)
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: line.Raw}, agent.SpanInfo{}); err != nil {
			slog.Error("acp persist notification", "agent_id", b.AgentID(), "method", line.Method, "error", err)
		}
	}
}

// PublishTurnActive satisfies Agent for every ACP provider. The base already
// republishes promptActive from one place, so this only gives that place the
// interface's name.
func (b *Base) PublishTurnActive() agent.TurnState {
	b.Mu.Lock()
	active := b.promptActive
	steerable := active && b.steersLocked()
	b.Mu.Unlock()
	b.notePromptActive()
	return agent.TurnState{Active: active, Steerable: steerable}
}

// IsCurrentSession reports whether sessionID is the session this agent is
// serving right now. Before the agent knows its session, which is during the
// handshake of the start, every session counts as the current one.
//
// It takes b.Mu only, because b.Mu is what guards b.sessionID: newSessionLocked
// writes the field under b.Mu and WithSessionID reads it under b.Mu.
//
// It must NOT take b.sessionMu. That lock is held across a whole session/new
// round trip, and only the reader goroutine can deliver the response to it.
// This function runs ON that reader goroutine, so an RLock here stops the
// reader until the round trip finishes, and the round trip cannot finish until
// the reader runs. ClearContext then hangs for the full API timeout, and every
// notification behind the blocked line waits with it.
func (b *Base) IsCurrentSession(sessionID string) bool {
	b.Mu.Lock()
	current := b.sessionID
	b.Mu.Unlock()
	return current == "" || current == sessionID
}
