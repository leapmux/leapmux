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
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/version"
)

// These JSON-RPC method constants serve every Agent Client Protocol (ACP) provider.
const (
	MethodInitialize                  = "initialize"
	acpMethodSessionUpdate            = "session/update"
	acpMethodSessionRequestPermission = "session/request_permission"
	MethodSessionCancel               = "session/cancel"
	// An agent sends this JSON-RPC notification to withdraw a request.
	// The providers use two spellings:
	//   - Goose uses `$/cancel_request`.
	//   - Language Server Protocol uses `$/cancelRequest`, the source of the same convention.
	// Both spellings identify the request through `params.requestId`.
	acpMethodCancelRequestSnake = "$/cancel_request"
	acpMethodCancelRequestCamel = "$/cancelRequest"
	MethodSessionNew            = "session/new"
	MethodSessionLoad           = "session/load"
	// MethodSessionResume opens a stored session without replaying it.
	// These providers resume through this method:
	//   - OpenCode.
	//   - Kilo.
	//   - Grok Build.
	//   - Qwen Code.
	// A session/load replay would draw the entire conversation in the transcript again.
	MethodSessionResume   = "session/resume"
	MethodSessionPrompt   = "session/prompt"
	MethodSessionSetModel = "session/set_model"
	MethodSessionSetMode  = "session/set_mode"
	// MethodSessionSetConfigOption identifies ACP's session/set_config_option setter.
	// Its params contain {sessionId, configId, value}, and its response contains the refreshed configOptions list.
	// It writes mutable option groups that have no dedicated set_model or set_mode channel.
	// Examples include OpenCode and Kilo's effort option and Goose's thinking_effort option.
	MethodSessionSetConfigOption = "session/set_config_option"

	// These ACP host terminal methods run from the agent to the client.
	// clientCapabilities.terminal advertises them. See terminal.go.
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
	// interruptRequested records that the reader stopped the current turn.
	// A later tool result then reports that stop instead of the provider's original status.
	// Cursor and Reasonix send `failed` for a cancelled command.
	// OpenCode and Kilo send an empty `completed` result.
	// Without this field, the row reports a failure caused by the reader's stop or reports nothing.
	// b.Mu protects the field. See noteACPInterruptRequested.
	interruptRequested bool
	// agentTurnActive records a turn that the agent starts through BeginAgentTurn, without a session/prompt from LeapMux.
	// promptActive also becomes true, so every active-turn check reads one flag.
	// b.Mu protects the field.
	agentTurnActive bool
	// agentTurnQueued records an agent turn that starts before the base processes the preceding prompt's end.
	// The prompt's end transfers the busy state to that turn. See BeginAgentTurn.
	// queuedAgentTurnEnd holds the agent turn's end frame when that frame arrives before the prompt's end.
	// b.Mu protects these fields.
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
	sink agentapi.ProviderServices
	// hooks holds the provider's changes to this base.
	// applyHooks sets it once before the process starts.
	// Nothing changes it afterwards, so the base reads it without b.Mu.
	hooks Hooks
	acpTurnOutput
	acpTerminalHost
	// children routes the updates of each subagent into its own transcript. See
	// children.go.
	children acpChildren
	// availableCommands holds the last command set that the ACP process advertises.
	// Providers that support goals read their command token from it.
	//
	// The set belongs to the process, so ClearContext preserves it while clearing other session fields.
	// Goose advertises it once in the first session/prompt, never in a session/new reply.
	// Clearing it would disable a working control until the user's next message.
	// A late update from a replaced session can only repeat the commands that the same binary offers.
	// Claude's hasGoalCommand describes the binary for the same reason.
	availableCommands map[string]struct{}
	// subagentPrompts holds each spawn prompt until its child transcript exists. See SubagentObservation.Prompt.
	// The registry RowKey identifies each entry, and subagentPromptMu protects the map.
	// Child creation consumes an entry, and row closure removes it.
	// A provider that never links a child therefore cannot grow the map without a limit.
	subagentPrompts providerkit.PendingPrompts
	// secondaryChannelOnce and secondaryChannelCache retain the resolved secondary channel.
	// applyHooks sets hooks.ModeChannel at construction.
	// The channel's field pointers and closures capture the stable Base pointer, so the resolution stays constant for the agent's lifetime.
	// secondaryChannel() creates the channel on its first use after applyHooks sets the mode channel.
	// It does not recreate the closure structure for each of its approximately seven operation callers.
	// Base always uses a pointer, so callers never copy the channel.
	secondaryChannelOnce  sync.Once
	secondaryChannelCache acpSecondaryChannel
	reapplySettings       func()                // called by ClearContext after session/new to re-apply model, mode, etc.
	refreshFromSession    func(json.RawMessage) // called by ClearContext after reapplySettings to sync state from the session response
	sessionID             string
	workingDir            string
	model                 string
	permissionMode        string
	currentPrimaryAgent   string
	availableModels       []*agentapi.ModelInfo
	// modelsFieldInfos holds the SessionModelState models from the last full handshake or ClearContext response.
	// A runtime config_option_update contains only the configOptions model selector.
	// applyConfigOptionModelsLocked combines both sources, so providers with a split catalog retain models reported only through SessionModelState.
	modelsFieldInfos       []ModelInfo
	availableModes         []*leapmuxv1.AvailableOption
	availablePrimaryAgents []*leapmuxv1.AvailableOption
	// secondaryFallback holds this provider's static permission modes or primary agents.
	// OptionGroups returns this list before the session reports its catalog.
	// Start reads the list from the provider's static groups.
	// It remains nil for a provider with no list, such as Reasonix.
	// The shared OptionGroups therefore serves every ACP family without a provider override.
	// StaticSecondaryGroup uses the same fallback during registration.
	secondaryFallback []*leapmuxv1.AvailableOption
	// options holds the server's config options that the model and mode channels do not own.
	// b.Mu protects all option state alongside the other Base fields.
	// A refresh can therefore pair an option change with its model or secondary-setting change in one critical section. See optionState.
	// The option state intentionally has no separate mutex.
	options optionState
	// optionWriteMu serializes each complete option-write batch against other batches.
	// The batch operations are:
	//   - applyOptionUpdates.
	//   - reapplyOptions.
	//   - applyStartupOptions.
	// Concurrent batches cannot interleave their session/set_config_option RPCs or validate IDs against an incomplete map.
	// This operation lock differs from the b.Mu state lock.
	// Acquire it before b.Mu, never in the reverse order, to prevent a lock cycle.
	//
	// It intentionally does not protect handleACPConfigOptionUpdate.
	// That handler runs on the reader goroutine that delivers the RPC responses.
	// Blocking the reader on an active batch would deadlock.
	// A server config_option_update can still arrive during a batch.
	optionWriteMu sync.Mutex
	// sessionMu serializes the session lifecycle against each session RPC.
	// newSessionLocked holds its write lock through session/new and the sessionID swap.
	// These operations hold its read lock through WithSessionID while capturing sessionID and sending the request:
	//   - SetModelViaConfigOption.
	//   - acpSetMode.
	//   - setConfigOption.
	//   - cancelSession.
	// Without the lock, a request could capture the old sessionID and send after ClearContext replaces that session.
	// The request would then target a retired session.
	// The lock order is optionWriteMu -> sessionMu -> b.Mu.
	// ClearContext releases sessionMu before reapplySettings, whose RPCs acquire the read lock separately for each call.
	sessionMu      sync.RWMutex
	sessionUpdates acpSessionUpdates
}

// handleACPPromptResponse drains one turn and persists its prompt response.
func (b *Base) handleACPPromptResponse(resp json.RawMessage) {
	if resp == nil {
		b.finishIncompleteACPPrompt(agentapi.MessageCompletionInterrupted)
		return
	}
	b.persistFinishedTurn(resp)
}

// persistFinishedTurn drains the turn that ended with frame and persists frame as its turn-end row.
// frame is the agent's own end record:
//   - A session/prompt response for a turn that LeapMux starts.
//   - The provider's end frame for a turn that the agent starts itself.
//
// A prompt with a queued agent turn drains only its own output. See BeginAgentTurn.
// The boundary already contains the prompt's text.
// The live text counter now counts the agent turn, so that turn stays open.
func (b *Base) persistFinishedTurn(frame json.RawMessage) {
	main := b.main()
	turn, hasPromptBoundary := b.drainPromptTurn()
	if !hasPromptBoundary {
		main.persistCompletedText(agentapi.AssembledMessageKindReasoning, turn.thoughtText)
		main.persistCompletedText(agentapi.AssembledMessageKindText, turn.assistantText)
	}
	// An unfinished tool fails unless the reader stopped the turn, which interrupts the tool instead.
	// A clean cancel returns a prompt response rather than an error.
	// Cursor therefore enters this path, not the error path.
	// Its stopped command otherwise stores `completion: error` for work that the reader chose to end.
	incomplete := agentapi.MessageCompletionError
	if b.acpInterruptRequested() {
		incomplete = agentapi.MessageCompletionInterrupted
	}
	main.persistIncompleteTools(turn.incompleteTools, incomplete)
	b.clearCompletedTerminals()
	numToolUses := turn.completedToolUses + len(turn.incompleteTools)

	b.persistPromptResponse(frame, numToolUses)
}

// acpTextProgressScope is the live-counter scope for one text kind. The scope is
// LeapMux's own key, so it keeps the protocol's update name rather than changing
// with the stored shape.
func acpTextProgressScope(kind agentapi.AssembledMessageKind) string {
	if kind == agentapi.AssembledMessageKindReasoning {
		return contracts.ACPUpdateAgentThoughtChunk
	}
	return contracts.ACPUpdateAgentMessageChunk
}

// finishIncompleteACPPrompt ends a prompt with an empty response or a failed request.
// A prompt with a queued agent turn closes only the tool calls that the prompt left open.
// The agent turn retains its progress. See BeginAgentTurn.
func (b *Base) finishIncompleteACPPrompt(completion agentapi.MessageCompletion) {
	turn, hasPromptBoundary := b.drainPromptTurn()
	if !hasPromptBoundary {
		b.finishACPTurn(turn, completion)
		return
	}
	if b.IsDiscardingOutput() {
		return
	}
	b.main().persistIncompleteTools(turn.incompleteTools, completion)
	b.clearCompletedTerminals()
}

// finishAllTurnOutput ends output for both the prompt and an agent turn queued behind it.
// Stop and process exit end both turns.
func (b *Base) finishAllTurnOutput(completion agentapi.MessageCompletion) {
	b.finishACPTurn(b.drainTurn(), completion)
}

func (b *Base) finishACPTurn(turn acpTurnSnapshot, completion agentapi.MessageCompletion) {
	if b.IsDiscardingOutput() {
		b.ResetCumulativeOutput()
		b.sink.ReportProgress(agentapi.ResetProgress())
		return
	}
	b.main().finishTurn(turn, completion)
	b.clearCompletedTerminals()
	b.sink.ReportProgress(agentapi.ResetProgress())
}

// The shared ACP dispatcher calls MethodHandler for JSON-RPC methods that it does not handle.
// Return true when the handler consumes the method.
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
	// A provider can attach a subagent tag to an update on the main session.
	// The update belongs to that subagent's transcript, so the main session reads none of it.
	// Its metadata also belongs to the subagent's usage and progress.
	// An update whose tag identifies no child transcript stays in the main transcript, where the reader can still see it.
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
		// Every ACP provider shares the model channel.
		// Each provider selects its own mode behavior.
		b.handleACPConfigOptionUpdate(update)
	case contracts.ACPUpdateCurrentMode:
		b.handleACPModeUpdate(update)
	case contracts.ACPUpdateUserMessageChunk:
		// No-op: user_message_chunk is history replay.
	case contracts.ACPUpdateAvailableCommandsUpdate:
		b.observeAvailableCommands(update)
	case contracts.ACPUpdateSessionInfoUpdate:
		// Session metadata contains the runtime's title and modified time.
		// Neither field belongs to the conversation, and each turn supplies an update.
		// Persisting those updates put a raw JSON row in every transcript.
		// A provider with its own metadata field uses sessionMetadataHandler before this switch.
		// Goose reads its steer-run identifier there.
		//
		// The runtime computes a real title, while LeapMux assigns its own tab names.
		// Adopting the runtime title requires a presentation decision, so this handler leaves it unread.
	}
}

// ApplySubagentObservation applies a provider observation outside a main-session tool call.
// The observation can report a subagent spawn or end, or the progress of a workflow run.
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
// The base reads standard available_commands_update notifications.
// A provider that receives commands elsewhere reports that list here also.
// Grok Build supplies it in the initialize response before any update.
// The goal control therefore need not wait for the first prompt.
// A change can add or remove a goal command, so the method publishes goal capabilities again.
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

// ClearContext replaces the current session through session/new on the existing ACP process.
// The outgoing session must perform no work that LeapMux does not show. See session_retirement.go.
// Before session/new, the method releases that session:
//   - Answer each open control request.
//   - Send session/cancel when a turn runs.
// After the swap, close the outgoing session's rows and end that session with the agent.
// Then reapplySettings, when present, applies the provider settings to the new session, such as the model and permission mode.
// A failed session/new leaves the outgoing session current with its turn stopped, as Stop does.
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
	b.finishACPTurn(outgoingTurn, agentapi.MessageCompletionInterrupted)
	// Remove each unconsumed spawn prompt also.
	// Its row belongs to the outgoing session, which will supply no closing observation to remove it.
	// Keeping it would retain it for the agent process's lifetime.
	// A reused tool-call ID in the new session would then open its transcript with the preceding session's instruction.
	// Codex clears its corresponding map here also.
	b.subagentPrompts.Clear()
	// A provider with state indexed by tool-call ID has the same risk and clears that state here.
	if b.hooks.ClearProviderState != nil {
		b.hooks.ClearProviderState()
	}
	// A session/new reply with the outgoing ID still serves that session.
	// The method therefore retires none of its state.
	if outgoingSessionID != "" && outgoingSessionID != sessionID {
		b.retireSession(outgoingSessionID)
	}

	// A goal belongs to a session, and this call replaces that session.
	// Codex and ZCode clear the goal in their ClearContext methods for the same reason.
	// This ACP implementation covers every provider on the base; Reasonix currently reports goals.
	//
	// A provider that reports no goal causes no write.
	// clearGoal reads the row first and returns before the broadcast when no goal exists.
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
			// The swap ends the preceding session's turn and clears every field owned by that turn.
			// The interrupt note is one such field.
			// Keeping it would mark every completed tool row in the next turn as Interrupted.
			// ClearContext reaches this path without calling clearActivePrompt.
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
// Each axis declares these values once:
//   - The option ID.
//   - The label.
//   - The order.
// Permission mode uses "Mode". The primary agent uses "Primary Agent".
// secondaryChannel and StaticSecondaryGroup share that declaration, so their presentation cannot differ.
type secondaryAxis struct {
	optionID string
	label    string
	order    int32
}

var (
	permissionModeAxis = secondaryAxis{optionID: agentapi.OptionIDPermissionMode, label: "Mode", order: agentapi.OptionOrderPermissionMode}
	primaryAgentAxis   = secondaryAxis{optionID: agentapi.OptionIDPrimaryAgent, label: "Primary Agent", order: agentapi.OptionOrderPrimaryAgent}
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
// The result holds these channel members:
//   - Its presentation.
//   - Its state pointers.
//   - Its refresh operations.
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

// UpdateSettings applies these settings for every ACP family:
//   - The model.
//   - The secondary setting.
//   - Mutable config options.
// It uses the resolved secondary channel and effectiveSetModel.
// b.hooks.ModelIDNormalizer supplies each provider's model-ID conversion.
func (b *Base) UpdateSettings(options optionmap.Map) agentapi.SettingsApplyResult {
	sc := b.secondaryChannel()
	model := options[agentapi.OptionIDModel]
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
	persistsBefore := b.options.persists
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
	refreshedDuringWrites := b.options.persists != persistsBefore
	sessionID := b.sessionID
	b.Mu.Unlock()
	// A server can send config_option_update before answering the model write.
	// The base then persists the value that the server resets.
	// A write that restores the user's value can send no notification, leaving no later refresh to replace the stored reset value.
	// Persist the final state again, so the stored row equals the session state.
	// applySessionRefresh also persists after reapply during a context clear.
	// A failed batch causes a restart, which persists its own confirmed settings.
	//
	// Live agents always have b.sink. Tests can create Base without a sink.
	// Keep the nil guard so a catalog change in such a test cannot panic.
	if ok && refreshedDuringWrites && b.sink != nil {
		b.BroadcastSettingsRefresh()
	}
	if optionGroupsChanged && b.sink != nil {
		b.sink.BroadcastStatusActive(sessionID)
	}
	if !ok {
		return agentapi.RestartRequiredSettings(options)
	}
	return b.SettingsSnapshot()
}

func (b *Base) SettingsSnapshot() agentapi.SettingsApplyResult {
	result := agentapi.ConfirmedSettings(agentapi.CurrentOptions(b.OptionGroups()))
	b.Mu.Lock()
	unresolved := b.options.unresolved.keys()
	b.Mu.Unlock()
	for _, id := range unresolved {
		result.Settlements[id] = agentapi.OptionSettlement{State: agentapi.OptionSettlementUnresolved}
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
	// Parse resp once to derive the combined available-model list and the current model and secondary-setting IDs.
	var model, secondaryVal string
	var models []*agentapi.ModelInfo
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
func (b *Base) refreshModelsLocked(models []*agentapi.ModelInfo, modelsFieldInfos []ModelInfo, model string, normalizeModel func(string) string) {
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
// It selects the first available choice in this order:
//   - A valid reported value.
//   - A valid stored value.
//   - The first nonempty option ID.
// An empty rebuild preserves the preceding list.
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
	return map[string]string{agentapi.OptionIDPrimaryAgent: a}
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

// buildPrimaryAgentOptions converts the handshake modes channel into primary-agent options.
// It normalizes names because OpenCode-family agents often report name == id or names containing only whitespace.
// It skips IDs that primaryAgentHiddenFilter marks as hidden.
// OpenCode hides its compaction, title, and summary pseudo-agents through that filter.
func (b *Base) buildPrimaryAgentOptions(modes []ModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	normalized := make([]ModeInfo, len(modes))
	copy(normalized, modes)
	for i := range normalized {
		normalized[i].Name = normalizeOptionName(normalized[i].Name, normalized[i].ID)
	}
	return buildACPModes(normalized, currentModeID, b.hooks.PrimaryAgentHiddenFilter)
}

// defaultOrFirstOption returns the first nonempty option ID, or "" when none exists.
// reconcileCurrentOptionID uses it to initialize permission mode or the primary agent when the server reports no valid current selection.
// ACP options carry no default badge for an individual option.
// The group's current value supplies the authoritative selection, so the first option initializes the selection.
func defaultOrFirstOption(options []*leapmuxv1.AvailableOption) string {
	for _, option := range options {
		if option != nil && option.Id != "" {
			return option.Id
		}
	}
	return ""
}

// reconcileCurrentOptionID resolves permission mode or the primary agent against a newly built option list.
// The current value must belong to that list.
// Select the first usable value in this order:
//   - A nonempty reported value that the list contains.
//   - A stored value that the list still contains.
//   - The list's first nonempty option ID.
//
// An empty list means that the session did not report this channel again.
// It does not mean that no value is valid.
// Keep a nonempty reported value, or otherwise the stored value, unchanged in this case.
// This matches acpSetMode's len(available)>0 guard.
// These paths share the resolver and therefore select the current value consistently:
//   - The handshake through configurePrimaryAgents.
//   - Runtime updates through syncConfigOptionSelectLocked.
//   - ClearContext through applySessionRefresh.
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

// acpApplySetting skips empty values.
// It logs a warning and returns false when the write fails.
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

// acpStandardInitParams encodes the initialize params that every ACP provider shares:
//   - Protocol version 1.
//   - LeapMux clientInfo.
//   - clientCapabilities.
//
// The hooks control each provider's capabilities.
// clientCapabilities.terminal is true unless Hooks.DisableHostTerminal disables host terminals.
// Agents that honor this capability, such as Goose and Reasonix, route shell commands through terminal/*.
// The handlers in terminal.go expose KindShell background-task rows.
// Providers that run their own shell commands disable this capability.
//
// clientCapabilities.fs.* is true unless Hooks.DisableHostFileSystem disables the host filesystem.
// Agents that edit through the client, such as Dirac's edit_file, read and write the same file state as the host.
// The handlers live in fs.go. See issue #370.
// Providers that require their own filesystem disable both filesystem capabilities.
// Hooks.ClientCapabilityMeta and Hooks.InitializeMeta supply the two `_meta` objects.
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
// When present, adjust changes the params before encoding. See Hooks.SessionParams.
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

// wireTurnActive connects the base's turn-state hook to b.sink.
//
// Start calls it once, and every ACP provider uses that constructor.
// No provider must connect this hook separately.
// The tests call this method also, so they cannot verify a separate hook that the constructor never installs.
//
// The hook reads b.sink again on every call and captures no separate sink.
// startACPHandshake replaces b.sink through NewModelProgressResetSink.
// A hook that captured the original sink would bypass every later decorator for the process lifetime.
// The turn flag controls input-queue dispatch alongside the queue's other conditions.
// A decorator that changes SetTurnState must receive these calls, or later input can stay queued without a report.
func (b *Base) wireTurnActive() {
	b.publishTurnActive = func(active bool, seq uint64) {
		b.Mu.Lock()
		steerable := active && b.steersLocked()
		b.Mu.Unlock()
		providerkit.PublishTurnStateTo(b.sink, agentapi.TurnState{Active: active, Steerable: steerable}, seq)
	}
}

// steersLocked reports whether the agent supports steering through an advertised method or a provider-owned route.
// SupportsSteering and each published turn's steerable flag use this same result.
// The Steer control and input queue therefore cannot disagree.
// The caller holds b.Mu.
func (b *Base) steersLocked() bool {
	return b.steerMethod != "" || b.hooks.SteersByOwnRoute
}

// notePromptActive publishes the turn state from promptActive, the single source.
// Call it after every critical section that writes promptActive.
//
// The method reads the field again instead of accepting a supplied value.
// A caller therefore cannot publish a value that differs from the field.
// Only a missing call can make them differ.
// Never call it while holding b.Mu, because the hook broadcasts and a slow transport can block that broadcast.
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

// SupportsSteering reports whether the agent supports an advertised steer method or a provider-owned route through Hooks.SteersByOwnRoute.
// An ACP server advertises the method in its initialize response.
// A provider that uses that method can therefore answer only after the handshake.
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
		return agentapi.ErrSteeringUnsupported
	}
	if !active {
		return agentapi.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    BuildPromptBlocks(content, agentapi.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if _, err := b.SendRequest(method, params, b.APITimeout()); err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agentapi.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	b.Mu.Lock()
	stillActive := b.promptActive
	b.Mu.Unlock()
	if !stillActive {
		return agentapi.ErrNoActiveTurn
	}
	return nil
}

// Stop performs these operations:
//   - Clear prompt state.
//   - Release host terminals.
//   - Stop the agent process.
func (b *Base) Stop() {
	b.NoteIntentionalStop()
	b.clearActivePrompt()
	b.releaseAllTerminals()
	b.Process.Stop()
	b.finishAllTurnOutput(agentapi.MessageCompletionInterrupted)
	b.finishAllChildConversations()
}

// Wait blocks until the agent process exits, then releases host terminals that remain after a crash or natural exit.
// Stop already releases them when the caller intentionally stops the process.
// releaseAllTerminals is idempotent.
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

// stopAndWait stops the agent process and blocks until it exits.
// The handshake and every Start* path use it to stop an incompletely initialized agent after a fatal startup error.
func (b *Base) stopAndWait() {
	b.Stop()
	_ = b.Wait()
}

// noteACPInterruptRequested records a stop for the current running turn.
//
// A stop on an idle agent records nothing because no turn needs interruption.
// A retained note would incorrectly mark the next turn's first result as interrupted.
func (b *Base) noteACPInterruptRequested() {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.promptActive {
		b.interruptRequested = true
	}
}

// acpInterruptRequested reports whether the reader stopped the current turn.
//
// It reads the note without consuming it.
// One stop interrupts every active tool, and each tool persists its own row.
// The turn's end removes the note.
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

// resetTurnStateLocked clears every field that belongs to one turn.
// The caller must hold b.Mu.
// This method defines the complete set once, so a session swap and turn end cannot clear different subsets.
// The preceding session-swap implementation cleared two of three fields and retained the interrupt note.
//
// This method does not publish state.
// clearActivePrompt calls notePromptActive after releasing b.Mu.
// The session swap publishes state after releasing the session locks.
// notePromptActive broadcasts, and a broadcast must not run under either lock.
//
// Remove the note when its turn ends, rather than at the next turn's start.
// handleToolCallUpdate reads it without checking promptActive, and the prompt response runs on a separate goroutine.
// Otherwise a tool update after the turn's end would report an interruption for as long as the tab stays idle.
func (b *Base) resetTurnStateLocked() {
	b.promptActive = false
	b.agentTurnActive = false
	b.agentTurnQueued = false
	b.queuedAgentTurnEnd = nil
	b.steerRunID = ""
	b.interruptRequested = false
}

// extractACPChunkText reads the text field from an ACP content envelope.
// It returns "" in each of these cases:
//   - The field is absent.
//   - The field is empty.
//   - Decoding fails.
// It logs a warning when decoding fails.
// The kind argument identifies the session update type in that warning.
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
	if err := b.sink.PersistTurnEnd(agentapi.WithToolUseCount(agentapi.MessageContent{Original: resp}, numToolUses), agentapi.SpanInfo{}); err != nil {
		slog.Error("persist acp prompt result", "agent_id", b.AgentID(), "error", err)
	}
	b.sink.ResetSpans()
}

// ToolCallEnvelope holds a parsed ACP session/notification tool_call.
// It carries these fields that the preceding parser omitted:
//   - Title.
//   - RawInput.
//   - RawOutput.
//   - Meta.
// Provider hooks can therefore detect a subagent spawn from the input shape instead of guessing from its tool name.
type ToolCallEnvelope struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Meta       json.RawMessage `json:"_meta"`
}

// ToolOutputObservation holds all output facts that one live update supplies for a running call.
//
// One value replaces two hooks because the byte count and text describe the same accumulated buffer.
// If a reader received one value before a chunk and the other afterwards, the row would contradict itself.
type ToolOutputObservation struct {
	// Total is how many bytes the call produced.
	Total int64
	// TotalIsMinimum states that Total is a minimum because the call lost output before this update.
	TotalIsMinimum bool
	// Tail supplies the text that the running row draws.
	// It remains empty when the update's own content carries the provider's output, which the shared content path reports instead.
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
	// ModeUpsert is the default mode.
	// It upserts the registry row, optionally persists a child transcript payload, and closes the row when CloseRow is set.
	// Detectors with descriptive fields use this mode for spawn, tool-request, and progress observations.
	ModeUpsert acpObservationMode = iota
	// ModeCloseOnly skips the upsert and closes an existing row without creating one.
	// Closing-update detectors that run for every tool_call, such as Goose and Cursor, use this mode.
	// A plain tool's final update therefore cannot create an incorrect subagent row.
	ModeCloseOnly
)

// SubagentObservation is the neutral structure that a provider hook produces.
// It contains these parts:
//   - Registry upsert data: kind/rowKey/title/activity/status/group.
//   - An optional row close.
//   - An optional child transcript payload with the child key and raw bytes to persist.
// Shared code converts the observation into sink calls, so provider-specific names and shapes stay outside this file.
type SubagentObservation struct {
	// These fields describe the registry row.
	// RowKey is the provider linkage key, such as a toolCallId or child session ID.
	// An empty RowKey causes no registry write.
	RowKey string
	Kind   bgtask.Kind // defaults to Subagent
	Title  string
	// TitleIsCommand marks Title as the verbatim command of a shell row, which
	// a client sets as code. Only a provider that states the command itself sets
	// it (see bgtask.Item.TitleIsCommand).
	TitleIsCommand bool
	Activity       string
	Status         bgtask.Status
	// GroupKey identifies the row's group, and GroupLabel supplies that group's label.
	// A provider with a subagent workflow groups the workflow row and each subagent under the run ID.
	GroupKey   string
	GroupLabel string
	// ChildAgentKey supplies the actual native child key when the provider knows it.
	// An empty key and empty ChildSpawnSpanID leave the observation registry-only.
	ChildAgentKey string
	// ChildSpawnSpanID requests a child from a native spawn before its native key arrives.
	ChildSpawnSpanID string
	// ChildAgentSessionID supplies the provider's native session for a new child.
	ChildAgentSessionID string
	// Prompt holds the spawn instruction when the provider's spawn payload supplies one.
	// It becomes the child transcript's first message, so the tab opens on the instruction instead of the reply.
	//
	// The prompt waits under RowKey until the child exists.
	// Some providers report the prompt before the child identity.
	// Goose identifies its child from the first forwarded tool request.
	// Closing an unlinked row removes its prompt.
	Prompt string
	// CloseRow gives the row its final Status after the upsert when true.
	CloseRow bool
	// Spawns identifies a subagent launch request, including a request with invalid arguments.
	// It owns no span, so a long run does not move concurrent tools one column right.
	//
	// An update that identifies a launch can also set Spawns. Ordinary progress
	// and completion observations leave it false. The provider supplies this
	// fact; shared code must not infer it from the registry fields.
	Spawns bool
	// Mode selects upsert or close-only behavior.
	// Its zero value selects ModeUpsert.
	// Set ModeCloseOnly explicitly when an observation has no descriptive fields and must close an existing row without creating one.
	Mode acpObservationMode
	// RenameFrom, when set, renames the existing row from RenameFrom to RowKey before the close.
	// Use it when a provider opens a row under one key and learns the stable child ID only in the final update.
	// OpenCode opens under toolCallId and learns the session ID in that final update.
	// One row then represents the entire lifecycle.
	// Creating a second RowKey row and separately closing RenameFrom would split one task across two keys.
	// If that separate close never occurs, the spawn row also remains open.
	RenameFrom string
	// A non-nil ChildTranscriptPayload persists in the child transcript through PersistChildMessage.
	// Goose uses it for tool requests.
	ChildTranscriptPayload []byte
	// Report is the final report that the provider returned to the parent. The
	// shared translator copies it into the child transcript. The parent keeps its
	// native tool result, so the report stays visible in both conversations.
	ReportID string
	Report   agentapi.SubagentReport
}

func parseACPToolCallUpdate(update json.RawMessage) (map[string]json.RawMessage, ToolCallUpdateEnvelope, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(update, &fields) != nil {
		return nil, ToolCallUpdateEnvelope{}, false
	}
	tcu, ok := decodeACPToolCallUpdate(fields)
	return fields, tcu, ok
}

// decodeACPToolCallUpdate reads the tool_call_update fields that shared ACP code processes.
//
// Every key comes from contracts/acp-protocol.json.
// ToolSupplement creates and matches the same map from the same tables.
// A separate handwritten key would keep the old spelling after the generated key changes.
// The update would then decode with no status, content, or title.
// The row would stop changing without a build error or log message.
//
// The _meta key has no constant because it is the protocol's extension member, not a field that LeapMux stores.
// No contract table therefore contains it.
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

// ObservationIsSpawn reports whether an observation states that this tool call starts a subagent.
// Each provider detector supplies that fact directly.
// Shared code reads one field instead of inferring a spawn from populated registry fields.
//
// The preceding inference treated an upsert of a Running row as a spawn, which asks a different question.
// Goose's subagent_tool_request updates an existing running subagent and upserts its running row.
// That inference incorrectly classified the update as a spawn and removed the enclosing tool call's span.
//
// Keep the RowKey guard.
// An observation that identifies no row writes nothing and must take no span.
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
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	case "cancelled":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusStopped
	}
}

// ToolCallBlock holds the {type, content:{type,text}} shape in ACP tool_call_update.content[].
// The outer type is usually "content", and the inner content carries the renderable payload.
type ToolCallBlock struct {
	Type    string `json:"type"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ToolCallText joins text from all {type:"content"} blocks whose inner content is {type:"text", text:...}.
// It returns "" when the payload supplies no text, including these cases:
//   - A status-only in_progress update.
//   - Content with images only.
//   - Unrecognized block types.
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

// startACPHandshake performs the shared ACP startup steps:
//   - Drain stderr.
//   - Prepare the scanner.
//   - Send the initialize request.
//   - Send a new or resume session request.
//   - Validate the session ID.
//   - Call UpdateSessionID and BroadcastStatusActive.
func (b *Base) startACPHandshake(
	stdout, stderr io.ReadCloser,
	opts agentapi.Options,
	initParams json.RawMessage,
	sessionCfg SessionConfig,
) (*SessionResult, error) {
	b.DrainStderr(stderr)

	// Install the progress-reset decorator once for every ACP provider.
	b.sink = agentapi.NewModelProgressResetSink(b.sink)
	b.beginSessionUpdates()

	scanner := agentapi.NewStdoutScanner(stdout)
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
	// The session/load reply separates replayed history from live output.
	// The Worker already stores the history sent before that reply, so the startup drain drops it. See acpSessionUpdates.
	// The reader records the boundary while routing the reply and immediately buffers each later line.
	// This goroutine wakes afterwards and cannot itself distinguish a live line from replayed history.
	//
	// session/resume sends no replay, so every update before its reply is live output.
	// OpenCode and Kilo forward output from each session client, and that output can arrive during this interval.
	var observeReply func(json.RawMessage, error)
	if opts.ResumeSessionID != "" && sessionMethod == MethodSessionLoad {
		observeReply = func(json.RawMessage, error) { b.markSessionReplayCutoff() }
	}
	sessionResp, err := b.SendRequestObserved(sessionMethod, json.RawMessage(sessionParams), timeout, observeReply)
	if err != nil {
		cleanup()
		if opts.ResumeSessionID != "" {
			return nil, b.FormatStartupError(sessionMethod, providerkit.ResumeFailedError(opts.ResumeSessionID, err))
		}
		return nil, b.FormatStartupError(sessionMethod, err)
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

// StartSpec configures Start for one ACP provider.
// Start runs the fixed launch and handshake sequence that every ACP agent shares.
// The specification supplies only the differences between providers.
type StartSpec[T any] struct {
	Registration   agentapi.Registration                            // launch and option metadata of the provider
	ProviderName   string                                           // process/log name, e.g. "cursor"
	BaseArgs       []string                                         // args after the binary, e.g. {"acp"}; a provider whose args depend on the launch options builds them at the call site (see reasonix.Start)
	RCMarkerEnvKey string                                           // provider rc marker stripped + re-added on a login shell (e.g. "KILO_CLIENT"); "" if none
	PinnedEnv      []string                                         // "KEY=value" assignments that REPLACE any inherited value and any value that the user's profile exports (see PinEnv and launch.WrapSpec.SetEnv); nil for none
	SessionConfig  SessionConfig                                    // zero value -> acpDefaultSessionConfig
	NewAgent       func() *T                                        // construct a zero-value concrete agent
	Base           func(*T) *Base                                   // accessor for the agent's embedded Base
	Configure      func(a *T, sink agentapi.ProviderServices) Hooks // the hooks of the provider; the start applies them before the process starts
	AfterHandshake func(*T, *SessionResult, agentapi.Options) error // post-handshake apply step; nil for none
}

// Start starts an ACP agent process and performs the initialize and session handshakes that every ACP provider shares.
// Only these StartSpec fields differ between providers:
//   - The binary and its arguments.
//   - An optional rc marker.
//   - The session configuration.
//   - The hooks that Configure returns.
//   - The settings step after the handshake.
func Start[T any](ctx context.Context, opts agentapi.Options, sink agentapi.ProviderServices, spec StartSpec[T]) (_ agentapi.Agent, retErr error) {
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
		// The login shell runs the profile after the worker supplies cmd.Env.
		// An export in that profile can replace a value specified only in cmd.Env.
		// The shell wrapper therefore sets the specified values again after the profile runs.
		SetEnv: spec.PinnedEnv,
	}
	if spec.RCMarkerEnvKey != "" {
		wrap.StripEnvKeys = []string{spec.RCMarkerEnvKey}
	}
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, wrap)

	// FilterEnv removes the provider rc marker from the inherited environment.
	// Add it again only for a login shell, so the child detects a LeapMux launch without inheriting a stale parent value.
	// The filter applies more broadly than the assignment.
	// This path therefore combines FilterEnv with the explicit value below instead of using PinEnv alone.
	env := cmd.Environ()
	if spec.RCMarkerEnvKey != "" {
		env = envutil.FilterEnv(env, spec.RCMarkerEnvKey)
		if opts.LoginShell {
			env = append(env, spec.RCMarkerEnvKey+"=1")
		}
	}
	// An explicitly specified value replaces the inherited environment value because LeapMux requires that exact value.
	// For example, inherited OPENCODE_ENABLE_QUESTION_TOOL=0 would disable the tool that the question handler serves.
	// The shell wrapper's SetEnv repeats the same values after the profile, so the user's profile cannot replace them either.
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
	// providerkit.NewProcess returns a fresh value.
	// The call on the assignment's right side exempts it from copylocks, so assigning the embedded Process copies no held lock.
	b.Process = providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: spec.ProviderName, ShutdownGrace: spec.Registration.ShutdownGrace, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: metaPrefix}, pipes, ctx, cancel)
	b.sink = sink
	b.bind(b)
	b.wireTurnActive()
	b.model = opts.Model()
	// Every ACP provider shares the default settings lifecycle hooks.
	// Relaunch and ClearContext restore settings through one hook, and session responses refresh them through the other.
	// Both derive the secondary channel and model writer from hooks.ModeChannel and hooks.ModelSetter.
	// One implementation therefore serves every family.
	// ClearContext still checks both hooks for nil because a test can create an agent without Start, which leaves both hooks absent.
	b.reapplySettings = b.reapplyModelAndSecondary
	b.refreshFromSession = b.applySessionRefresh
	if spec.Configure != nil {
		b.applyHooks(spec.Configure(a, sink))
	}
	// Initialize the secondary fallback from the provider's static groups after applyHooks sets the mode channel.
	// The provider registers those same groups, so it declares the fallback list once.
	// A provider without static groups, such as Reasonix, has no fallback and keeps nil here.
	b.secondaryFallback = SecondaryFallbackFrom(spec.Registration.OptionGroups, b.hooks.ModeChannel)

	if err := b.StartCmd(); err != nil {
		return nil, err
	}
	// The process now runs.
	// Every later startup failure must stop it because Start returns no Agent on error.
	// The caller therefore receives no handle through which to call Stop.
	// Cancelling the context stops its child process.
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
	// Every concrete ACP agent (*T) implements Agent through its embedded Base and its own overrides.
	// Assert that interface here so Start can remain generic over T.
	agent, ok := any(a).(agentapi.Agent)
	if !ok {
		return nil, fmt.Errorf("acp agent %T does not implement Agent", a)
	}
	return agent, nil
}

// OptionGroups returns one ACP provider's configuration groups in this order:
//   - The model group.
//   - The mapped permission-mode or primary-agent group, when a secondary channel exists.
//   - Any mutable option groups that the server reports.
// The secondary group carries its current value.
// Omit it when it has neither options nor a current value.
// One implementation serves every ACP family through secondaryChannel and the provider's secondaryFallback, so providers need no override.
func (b *Base) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.Mu.Lock()
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := agentapi.ModelOptionGroup(b.availableModels, b.model, agentapi.EffortSubGroups); mg != nil {
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

// applyLocalOptions writes each changed value in a local option group from Hooks.LocalOptionGroups.
// It returns false when a write fails.
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

// secondaryOptionGroupLocked builds the mapped permission-mode or primary-agent group with its live current value.
// Before the session reports its catalog, the group uses the static secondaryFallback list.
// The unmapped channel uses the permission-mode group also.
// It tracks permission mode through native modes instead of the configOptions mode selector.
// Return nil when the group has neither options nor a current value.
// The caller holds b.Mu.
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

// parseACPSessionResult parses the channels that every ACP session response shares:
//   - Model.
//   - Mode.
//   - configOptions.
// It serves session/new and resume handshakes and the ClearContext response.
// The caller validates the session ID.
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
	// Category identifies the semantic channel of an ACP config option.
	// The standard categories are model, mode, and thought_level; custom categories start with an underscore.
	// The specification describes id as opaque and states "MUST NOT be required for correctness".
	// Dispatch therefore accepts category and the known ID, with the known ID taking precedence. See acpConfigOptionByCategory.
	// When a provider omits category, the known ID still identifies its channel.
	Category string `json:"category"`
	// Type identifies the widget kind.
	// The ACP specification currently defines select, and isSelectableConfigOption also treats an empty type as select.
	// The parser ignores every other widget type.
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// CurrentValue holds the wire value as a string. See UnmarshalJSON.
	// A non-select widget can send that value through its own JSON type.
	CurrentValue string              `json:"currentValue"`
	Options      []ConfigOptionValue `json:"options"`
}

// UnmarshalJSON accepts JSON values for currentValue and stores their string representation.
// Every downstream reader uses a string, but a non-select widget can supply its own JSON scalar type.
// Dirac sends the auto_approve and yolo values as JSON booleans with type: "boolean".
// isSelectableConfigOption excludes those widgets from the option system, so no option control displays their values.
// The session parse must still succeed, as the Type field already requires ignoring unknown widget types safely.
// Convert the native value through these rules:
//   - Decode JSON strings into plain text.
//   - Keep null and absent values empty.
//   - Preserve other values as compact JSON, including objects and arrays.
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

// configOptionCurrentString converts a raw currentValue into the string that the option system stores.
// An absent or null value becomes "", and a JSON string becomes its decoded text.
// Other valid JSON values retain their compact JSON literal, including booleans and numbers as well as objects and arrays.
// If the supplied raw bytes are invalid JSON, preserve them unchanged as a string.
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

// buildACPModels converts ModelInfo entries into neutral agentapi.ModelInfo entries.
// When non-nil, normalize transforms every model ID and currentModelID before use.
//
// Keep the first entry for each final normalized ID.
// acpHandshakeModelInfos combines two channels and removes duplicate raw IDs before this step.
// A normalizer can still map distinct raw IDs to one final ID, such as Cursor's "default[]" -> "auto".
// Removing duplicates here prevents repeated final models from that conversion and from repeated IDs in one native channel.
func buildACPModels(models []ModelInfo, currentModelID string, normalize func(string) string) []*agentapi.ModelInfo {
	if normalize != nil {
		currentModelID = normalize(currentModelID)
	}
	result := make([]*agentapi.ModelInfo, 0, len(models))
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
		result = append(result, &agentapi.ModelInfo{
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

// acpConfigOptionCategoryModel and acpConfigOptionCategoryMode hold the standard ACP categories for the model and mode selectors.
// Dispatch accepts those categories and the known channel IDs.
// The known ID takes precedence when both signals identify different options.
const (
	acpConfigOptionCategoryModel = "model"
	acpConfigOptionCategoryMode  = "mode"
	// acpConfigOptionCategoryThoughtLevel identifies a reasoning-effort channel.
	// Examples include OpenCode and Kilo's effort and Goose's thinking_effort.
	// Its options sort from strongest to weakest.
	acpConfigOptionCategoryThoughtLevel = "thought_level"
)

// isSelectableConfigOption reports whether a config option is a supported value-list selector.
// The ACP specification currently defines select, and this method treats an empty type as select also.
// It ignores every other type so an unknown widget cannot reach a picker that supports only a value list.
func isSelectableConfigOption(o ConfigOption) bool {
	return o.Type == "" || o.Type == "select"
}

// acpConfigOptionContentLess defines a stable total order only to resolve duplicate IDs deterministically.
// Duplicate IDs violate the specification, so this order assigns no semantic meaning.
// It makes selection independent of the incoming slice order.
// Compare CurrentValue first, then the offered values joined in sorted order.
func acpConfigOptionContentLess(a, b ConfigOption) bool {
	if a.CurrentValue != b.CurrentValue {
		return a.CurrentValue < b.CurrentValue
	}
	return acpConfigOptionValuesKey(a) < acpConfigOptionValuesKey(b)
}

// acpConfigOptionValuesKey joins an option's offered values in sorted order to create a stable key.
// Duplicate-ID options therefore retain the same order regardless of how the server orders either value list.
func acpConfigOptionValuesKey(o ConfigOption) string {
	vals := make([]string, 0, len(o.Options))
	for _, v := range o.Options {
		vals = append(vals, v.Value)
	}
	slices.Sort(vals)
	return strings.Join(vals, "\x00")
}

// acpConfigOptionByCategory finds a selectable option through either the known channel ID or its standard category in one scan.
// The category supports servers with opaque IDs.
// The known ID takes precedence because a category can identify more than one setting.
// Junie assigns category: "mode" to Brave Mode beside the real mode selector.
// Selecting by category alone would lose the session mode.
//
// The ID is the protocol's own identifier.
// The requirement "MUST NOT be required for correctness" permits a server to omit it; it does not make a present ID advisory.
// One option with both signals needs no tie-break.
// Duplicate IDs select the content-smallest entry through acpConfigOptionContentLess.
// The selected channel therefore cannot change when a server reorders the same payload.
// Return the matched option and true, or a zero option and false when no selectable option contains either signal.
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

// acpModelInfosFromConfigOption converts a model config selector into ModelInfo entries and the current value.
// Callers can then use buildACPModels exactly as they do for the SessionModelState models field.
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

// acpHandshakeModelInfos returns the available models and current model ID from a session handshake.
// Servers report models through one or both of these channels:
//   - The SessionModelState models field.
//   - A model selector in configOptions, which OpenCode and Kilo use exclusively.
// Combine both channels, keep models-field entries first, and remove duplicate model IDs.
// Providers with split catalogs or a partial channel list therefore expose every model.
// Use the models field's current ID when present, or otherwise the config option's current value.
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

// mergeModelInfos combines two model lists and removes duplicate raw model IDs.
// Keep primary entries first and prefer their metadata over a secondary duplicate.
// Use it to combine SessionModelState models with the configOptions model selector at handshake and runtime.
// A config_option_update therefore cannot remove models reported only through SessionModelState.
// When primary is empty, return secondary unchanged.
// OpenCode and Kilo normally use that case because they do not use the models field.
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

// buildModels converts raw model data into neutral models through the provider's model-ID normalizer.
// It returns those models and the normalized current model ID.
// The handshake and runtime channels share this method, so both perform exactly the same normalization.
func (b *Base) buildModels(infos []ModelInfo, currentModelID string) ([]*agentapi.ModelInfo, string) {
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

// applyHandshakeModels combines both model channels and writes availableModels and the current model under b.Mu. See acpHandshakeModelInfos.
// startACPHandshake starts the reader before Start* finishes.
// A config_option_update immediately after session/new can therefore call applyConfigOptionModelsLocked concurrently with this write.
//
// Set the current model to the server's value, including "", instead of preserving the requested model.
// trySetStartupModel compares the requested model with this native current value and calls setModel when they differ.
// This includes a missing native model for an agent that accepts arbitrary IDs without advertising a list.
// Every ACP provider uses this handshake method.
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
	// Expose each config option that the model and mode channels do not claim.
	// Every ACP provider runs this shared handshake step under the existing lock.
	// The handshake snapshot supplies the initial state, so its CurrentValue takes precedence.
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

// applyStartupPermissionMode applies the requested permission mode during startup.
// An empty request or unchanged mode needs no write.
// A rejected explicit request returns an error and stops startup because that selection is mandatory.
//
// One rejection does not stop startup: the session cannot offer a safe default that LeapMux selects for a new session.
// Goose uses smart_approve as its safe default.
// A native build without that mode would otherwise fail every new session with "unknown mode" for a selection the user never requested.
// Keep the handshake's current mode in that case and log the fallback.
// An explicit --permission-mode request still stops startup on rejection instead of silently selecting a different mode.
// Claude applies the same safe-default fallback in applyStartupPermissionMode.
//
// Read the current mode under b.Mu, as trySetStartupModel reads b.model.
// startACPHandshake starts the reader before Start* reaches this point.
// The reader can concurrently change permissionMode through syncConfigOptionModeLocked for a permission-mode provider.
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

// applyHandshakeMode sets availableModes and permission mode from a session handshake under the lock.
// Use defaultMode when the server reports no mode.
// For ModeChannelPermissionMode, a mode config option overrides the native modes channel.
// For an unmapped provider, applyOptionGroupsLocked exposes that option as a mutable group instead.
// Runtime and ClearContext use the same distinction, so every path interprets the option consistently.
// These ACP providers track permission mode:
//   - Cursor.
//   - Goose.
//   - Grok Build.
//   - Kiro.
//   - Qwen Code.
//   - Reasonix.
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

// applySecondaryStartup performs the same post-handshake sequence for both secondary-channel families:
//   - Apply the handshake models.
//   - Configure the secondary channel.
//   - Attempt the requested model write.
//   - Apply the remaining startup options.
// Secondary-channel rejection stops the agent and returns a startup error.
// Model rejection does not undo the secondary channel or stop an otherwise valid session.
// One shared sequence preserves that order for both families.
// configureSecondary supplies the only family-specific step: permission mode or primary-agent configuration.
func (b *Base) applySecondaryStartup(handshake *SessionResult, opts agentapi.Options, requestedModel string, configureSecondary func() error) error {
	b.applyHandshakeModels(handshake)
	if err := configureSecondary(); err != nil {
		b.stopAndWait()
		return b.FormatStartupError(MethodSessionSetMode, err)
	}
	b.trySetStartupModel(requestedModel)
	b.applyStartupOptions(opts)
	return nil
}

// ApplyPermissionModeStartup configures permission mode after the handshake for these providers:
//   - Cursor.
//   - Goose.
//   - Grok Build.
//   - Kiro.
//   - Qwen Code.
//   - Reasonix.
// It reads the handshake mode channel and applies the requested permission mode.
// Cursor supplies its normalized model ID.
// trySetStartupModel uses effectiveSetModel, which automatically selects Cursor's native conversion through setCursorModel.
// See applySecondaryStartup for the shared order that writes the requested model after the secondary setting.
func (b *Base) ApplyPermissionModeStartup(handshake *SessionResult, opts agentapi.Options, defaultMode, requestedModel string) error {
	return b.applySecondaryStartup(handshake, opts, requestedModel, func() error {
		b.applyHandshakeMode(handshake, defaultMode)
		b.finishSessionUpdates()
		return b.applyStartupPermissionMode(
			opts.PermissionMode(), opts.NewSessionDefaultOptionIDs[agentapi.OptionIDPermissionMode])
	})
}

// ApplyPrimaryAgentStartup configures the available primary agents and requested persisted selection for OpenCode and Kilo after the handshake.
// It differs from ApplyPermissionModeStartup only in that secondary-channel configuration.
// Read the fallback from b.secondaryFallback, which Start sets from the provider's static groups before the handshake.
// Each provider therefore declares its primary-agent fallback once.
func (b *Base) ApplyPrimaryAgentStartup(handshake *SessionResult, opts agentapi.Options, defaultAgent string) error {
	return b.applySecondaryStartup(handshake, opts, opts.Model(), func() error {
		return b.configurePrimaryAgents(handshake.Modes, handshake.CurrentModeID, opts.Get(agentapi.OptionIDPrimaryAgent), b.secondaryFallback, defaultAgent)
	})
}

// handleACPConfigOptionUpdate processes a config_option_update notification.
// Every ACP provider uses the same model-channel handling.
// The resolved mode channel controls the configOptions mode selector:
//   - ModeChannelPermissionMode applies permission mode.
//   - ModeChannelPrimaryAgent applies the primary agent for OpenCode and Kilo.
// Expose each unmapped option as a mutable group.
// Update every channel under one lock, so a concurrent settings read cannot observe an incomplete update.
// Broadcast after releasing the lock.
//
// A changed model, primary agent, or option value persists and broadcasts complete settings once through BroadcastSettingsRefresh.
// A permission-mode-only change uses UpdatePermissionMode to persist and broadcast the mode with its chat notification.
// When another setting changes alongside permission mode, emit only the mode's chat notification.
// BroadcastSettingsRefresh already includes that mode in StatusChange.
// A second broadcast could temporarily expose an older model.
func (b *Base) handleACPConfigOptionUpdate(update json.RawMessage) {
	options := parseACPConfigOptions(update)
	if len(options) == 0 {
		return
	}

	b.Mu.Lock()
	b.options.clearUnresolved()
	oldMode := b.permissionMode
	modelChanged, listChanged := b.applyConfigOptionModelsLocked(options)
	// Apply permission mode or the primary agent through the resolved secondary channel.
	// secondaryChannel() owns the family-specific selection, so this path need not choose a separate Locked method.
	// Map the returned value and change flag to the corresponding operation:
	//   - Permission mode uses Notify or UpdatePermissionMode.
	//   - The primary agent uses a complete settings refresh.
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
	// Expose and synchronize each unmapped config option as a mutable group.
	// An option-value change persists through BroadcastSettingsRefresh below.
	// A changed option list without a value change uses the status-refresh branch.
	// The server update supplies the authoritative CurrentValue.
	optionValueChanged, optionListChanged := b.applyOptionGroupsLocked(options)
	sessionID := b.sessionID
	b.Mu.Unlock()

	switch {
	case modelChanged || primaryAgentChanged || optionValueChanged:
		// A changed model, primary agent, or option value persists and broadcasts complete settings in one StatusChange.
		// That response reads the live model list and includes the current mode and options.
		// The frontend therefore displays the native change immediately.
		// Persist an option value through BroadcastSettingsRefresh, not BroadcastStatusActive, so the options column retains the selection.
		b.BroadcastSettingsRefresh()
		if modeChanged {
			// The preceding StatusChange already includes the new mode.
			// Emit only the chat settings_changed notification, without another StatusChange.
			b.sink.NotifyPermissionModeChanged(oldMode, mode)
		}
	case modeChanged:
		// A permission-mode-only change uses one call for these operations:
		//   - Persist the mode.
		//   - Broadcast StatusChange with the live model list.
		//   - Emit the chat notification.
		b.sink.UpdatePermissionMode(mode)
	case listChanged || optionListChanged:
		// An available list changed without changing any current selection.
		// The changed list can describe models, modes, primary agents, or config options.
		// BroadcastSettingsRefresh would perform no write because PersistSettingsRefresh skips unchanged model, mode, and option values.
		// Broadcast a status refresh directly instead.
		// Its StatusChange reads the live model list and option groups, so the frontend receives the new options.
		b.sink.BroadcastStatusActive(sessionID)
	}
	// A provider can derive goal actions from the session's mode list.
	// Reasonix sets goals through its goal mode, so a new mode list can add or remove a goal action.
	// The live agent supplies that capability, and only an explicit publication broadcasts it.
	// Without this publication, the goal card would retain the actions from the first reported list.
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
	// Keep the len(models) > 0 guard.
	// An update that produces an empty list must not replace a populated catalog.
	// Showing the last known models is preferable to an empty picker, even when those models temporarily become stale.
	// The supported providers do not produce a genuinely model-less update.
	// Do not change this branch to clear the catalog for an empty update.
	if len(models) > 0 && !agentapi.ModelInfosEqual(b.availableModels, models) {
		b.availableModels = models
		listChanged = true
	}
	if current != "" && current != b.model {
		b.model = current
		modelChanged = true
	}
	return modelChanged, listChanged
}

// protoSliceEqual compares two proto-message slices by their order and every field of each entry.
// The model, mode, and primary-agent channels use it to distinguish a real list change from a repeated update.
// They therefore avoid a redundant broadcast.
// proto.Equal also compares new fields, so a later field addition cannot make a changed update appear unchanged.
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

// buildOptionValues converts one config selector's value list into proto AvailableOption entries.
// It applies these rules:
//   - Keep the first entry for each value, as buildACPModels does.
//   - Skip empty IDs and IDs excluded by the hidden filter.
//   - Normalize names as buildPrimaryAgentOptions does at handshake.
// Every option therefore renders identically before and after a config_option_update and in the mode and mutable-option channels.
// The caller sets the group's DefaultValue through buildOptionGroup, rather than setting a default on each option.
// buildConfigOptionSelect and applyOptionGroupsLocked share this builder.
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

// buildConfigOptionSelect converts a configOptions mode selector into proto options and its current value.
// Apply the hidden filter when present.
// Return ok=false when the payload contains no mode option.
// Permission-mode and primary-agent synchronization share this method and differ only in the field that stores the result.
func buildConfigOptionSelect(options []ConfigOption, hiddenFilter func(string) bool) (built []*leapmuxv1.AvailableOption, current string, ok bool) {
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, ConfigOptionIDMode)
	if !ok {
		return nil, "", false
	}
	return buildOptionValues(option, hiddenFilter), option.CurrentValue, true
}

// syncConfigOptionSelectLocked refreshes permission mode or the primary agent from a configOptions mode selector.
// It writes the available list and current value through the caller's supplied fields.
// It reports these results:
//   - The new value.
//   - Whether the current value changed.
//   - Whether the available list changed.
// When preferredFirst is nonempty, move that ID first before comparing lists.
// The rebuilt list then matches the handshake's order, and an unchanged catalog compares equal.
// The caller must hold b.Mu.
// syncConfigOptionModeLocked and syncConfigOptionPrimaryAgentLocked share this method with different hidden filters, preferred IDs, and target fields.
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
	// Keep the len>0 guard, as applyConfigOptionModelsLocked does for models.
	// An update that produces an empty list must not clear a populated picker.
	if len(built) > 0 && !protoSliceEqual(*available, built) {
		*available = built
		listChanged = true
	}
	// Resolve the current value against the available list, including any rebuild.
	// Select a usable reported value, then a surviving stored value, then the first nonempty option ID.
	// A removed current option or hidden reported value therefore cannot leave the picker with an unavailable selection.
	// Select a replacement instead of clearing the value to "", so both families can persist a nonempty selection.
	// A cleared primary agent would make primaryAgentOptions return nil and preserve storage while changing memory.
	// configurePrimaryAgents at handshake and applySessionRefresh during ClearContext use the same resolution.
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
		refresh[agentapi.OptionIDModel] = model
	}
	if mode != "" {
		refresh[agentapi.OptionIDPermissionMode] = mode
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
	b.options.persists++
	b.Mu.Unlock()
	b.sink.PersistSettingsRefresh(acpRefreshMap(model, mode, optionValues))
}

// buildACPModes converts ModeInfo entries into proto AvailableOption messages.
// When filter is non-nil, skip each mode for which filter returns true.
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

// SetModeViaConfigOption merges native config options and acknowledges the mode before the reader releases the waiter.
// A missing option snapshot remains unresolved.
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

// setConfigOptionGuarded adds an optional precondition to setConfigOption immediately before its checks and send.
// When non-nil, stillWanted runs under the same b.Mu lock as the advertised-option and offered-value checks.
// raiseEffortOffNone uses it to require that the live effort still equals the daemon's none/off default.
// The caller can then skip a write when a concurrent update changes that value.
// A false precondition succeeds without a write.
//
// handleACPConfigOptionUpdate also holds b.Mu.
// An update that acquires the lock first changes the value before the precondition and can prevent an obsolete write.
// An update can still arrive after the checks, before or during the asynchronous RPC.
// The native process orders those later operations.
// Do not hold b.Mu across the RPC to remove that remaining interval.
func (b *Base) setConfigOptionGuarded(configID, value string, stillWanted func() bool) error {
	// Check the option IDs from the latest complete advertised payload, not only the values already exposed with a concrete current selection.
	// An advertised option with an empty current value must still accept its stored preference before it appears as a group.
	b.Mu.Lock()
	known := b.options.known.has(configID)
	offered := b.options.offersValue(configID, value)
	// Check the precondition under the same lock as the checks below.
	// Another b.Mu holder then cannot invalidate it between those checks.
	wanted := stillWanted == nil || stillWanted()
	b.Mu.Unlock()
	if !known {
		return fmt.Errorf("unknown config option: %s", configID)
	}
	if !wanted {
		// A concurrent update changed the value that this write requires.
		// Succeed without a write instead of replacing the daemon's new selection.
		return nil
	}
	// Skip a value that the current option list does not offer instead of sending it regardless of that list.
	// A model change can leave the preceding model's effort, such as xhigh, in the merged options map.
	// The new model may not offer that value.
	// Sending it would cause a native rejection, fail the live edit, and make UpdateSettings restart the agent.
	//
	// Treat the skipped write as success.
	// The running session keeps its actual value, and applySettingsLive reads that value back into storage.
	// offersValue accepts a stored preference when no offered list exists, so this branch skips only an explicitly unavailable value.
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

// withOptionWriteBatch owns the optionWriteMu -> b.Mu lock order and clones the snapshots that every config-option batch needs.
// See optionWriteMu for the full lock discipline.
// An inverted lock order deadlocks.
// A missing clone races the reader when a server config_option_update arrives during the batch.
// Hold optionWriteMu for the complete batch to serialize it against other batches.
// Release b.Mu before fn runs because each ID's RPC acquires that lock separately.
// fn receives consistent snapshots of current values and advertised IDs.
// It must iterate those snapshots, not the live maps.
func (b *Base) withOptionWriteBatch(fn func(values map[string]string, known []string)) {
	b.optionWriteMu.Lock()
	defer b.optionWriteMu.Unlock()
	b.Mu.Lock()
	values := maps.Clone(b.options.values)
	known := b.options.known.keys()
	b.Mu.Unlock()
	fn(values, known)
}

// forEachOption iterates the sorted union of advertised IDs and IDs with exposed values through withOptionWriteBatch.
// For each ID, decide(id, current) supplies the requested value and whether to write it.
// Skip an empty value or want=false. Otherwise call applyConfigOption.
// The caller controls whether an unchanged value needs a write.
// reapply must write the same value again, so this helper must not skip unchanged values itself.
// Return the combined success result for every ID; callers that do not need it can discard it.
//
// applyOptionUpdates needs advertised IDs even when they have no current value.
// A live change to such an option must reach setConfigOption instead of silently skipping it.
// Otherwise UpdateSettings would report success and persist a value that the running session never applies.
// Only a later relaunch's applyStartupOptions, which also iterates advertised IDs, would apply it.
// An advertised option without a current value supplies "" here.
// reapplyOptions repeats the current value and therefore still skips that option through the empty-value guard.
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

// sortedOptionIDs returns the sorted union of advertised IDs and IDs with exposed values, without duplicates.
// The advertised set normally contains every value key.
// Including both sets also retains a valued ID that least recently used (LRU) eviction removes from the advertised set.
// No exposed selection is therefore omitted.
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

// applyConfigOption writes one value through session/set_config_option and logs its result through acpApplySetting.
// Sparse updates and ClearContext restoration both use it as forEachOption's apply operation.
// The write therefore has one implementation.
func (b *Base) applyConfigOption(id, value string) bool {
	return b.applyConfigOptionGuarded(id, value, nil)
}

// applyConfigOptionGuarded forwards an optional precondition to setConfigOptionGuarded. See that method.
// raiseEffortOffNone uses it to check rank 0 under the write's own b.Mu lock, instead of checking earlier.
func (b *Base) applyConfigOptionGuarded(id, value string, stillWanted func() bool) bool {
	return acpApplySetting(b.ProviderName(), b.AgentID(), id, value, func(val string) error {
		return b.setConfigOptionGuarded(id, val, stillWanted)
	})
}

// applyOptionUpdates writes each value in a sparse settings update that differs from the current mutable config option.
// It sends session/set_config_option requests in sorted ID order for deterministic logs.
// Return false when any write fails, which the caller interprets as an incomplete update.
// The agent remains usable, and a rejected option keeps its preceding value.
func (b *Base) applyOptionUpdates(options map[string]string) bool {
	return b.forEachOption(func(id, current string) (string, bool) {
		v, present := options[id]
		return v, present && v != current
	})
}

// reapplyOptions restores the user's stored config options after ClearContext sends session/new.
// It corresponds to reapplyModelAndSecondary for model and mode selections.
// The user's effort, reasoning-effort, or allow-all selection therefore survives a context clear.
// stored is the snapshot that reapplyModelAndSecondary captures before restoring the model.
func (b *Base) reapplyOptions(stored map[string]string) {
	// Write the stored value even when it equals the current value.
	// session/new resets the server to its default, so an equality check would skip every restoration write.
	// Read stored instead of the live values, which the model write and raiseEffortOffNone replace with fresh-session defaults.
	// Those live values no longer represent the user's selection.
	// forEachOption still skips an empty stored value.
	b.forEachOption(func(id, _ string) (string, bool) { return stored[id], true })
}

// applyStartupOptions attempts requested config-option values after the handshake exposes the server's options.
// A fresh process starts with server defaults, so this step restores persisted preferences such as reasoning effort.
// Like trySetStartupModel, it logs and skips a rejected value without stopping an otherwise valid session.
func (b *Base) applyStartupOptions(opts agentapi.Options) {
	// A daemon can use a reasoning-effort ID other than effort, such as Goose's thinking_effort or a custom thought_level category.
	// resolveProviderDefaults and EffortEnvOverride store the operator's environment override under the known effort ID.
	// Resolve the native channel ID once so the loop maps that override onto the actual channel.
	// This matches the model and mode ID fallback and restores the default regardless of the daemon's ID.
	effortID := b.startupEffortConfigID()

	// Iterate every advertised option, not only options with exposed current values.
	// An option with an empty current value at handshake still needs its stored preference applied.
	// A fresh restarted process must not remain on the server default merely because that current value is unresolved.
	b.withOptionWriteBatch(func(values map[string]string, known []string) {
		for _, id := range slices.Sorted(slices.Values(known)) {
			requested := opts.Get(id)
			// An advertised effort channel with a different ID has no value under that ID in opts.
			// Use the known effort override so the request still applies.
			if requested == "" && id == effortID {
				requested = opts.Get(agentapi.OptionIDEffort)
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

// acpPermissionCancelAnswer is the protocol outcome for a permission request withdrawn by the client without a reader decision.
// It does not invent a decision that the reader never made.
func acpPermissionCancelAnswer() any {
	return map[string]any{"outcome": map[string]any{"outcome": contracts.ACPPermissionOutcomeCancelled}}
}

// cancelSession sends a session/cancel notification for the current session.
func (b *Base) cancelSession() error {
	return b.WithSessionID(b.sendSessionCancel)
}

// Interrupt aborts the active ACP turn through session/cancel.
// Every supported ACP server recognizes that notification, and Provider.IsInterrupt expects the same shape.
//
// Every ACP agent embeds Base, so this implementation serves every ACP provider.
// If sessionID is still empty, the method does nothing.
// The worker InterruptAgent RPC can therefore call it without waiting for the handshake.
func (b *Base) Interrupt(stop agentapi.StopContext) error {
	if b.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	b.Mu.Lock()
	sessionID := b.sessionID
	b.Mu.Unlock()
	if sessionID == "" {
		return nil
	}
	// Record the stop before sending the cancel.
	// A result sent immediately after the provider receives the cancel then already belongs to the stopped turn.
	b.noteACPInterruptRequested()
	if b.hooks.CancelBeforeControlWithdrawal {
		if err := b.cancelSession(); err != nil {
			return err
		}
		b.withdrawTurnControls()
		return nil
	}
	// Other providers block their cancel on an open control. Release that control first.
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

// handleACPCancelRequest withdraws a control request that the agent cancels.
//
// The protocol notification writes no transcript row.
// Without this handler, the shared dispatcher's default branch persisted the raw frame.
// A reader who stopped a turn then saw JSON-RPC where the withdrawn request previously appeared.
//
// This withdrawal differs from the cancelled outcome that LeapMux sends for its own interrupt. See WithdrawAllControlRequests.
// An agent can independently cancel a request, and this handler covers that case.
// Send no answer because the agent already withdrew its request and waits for none.
func (b *Base) handleACPCancelRequest(params json.RawMessage) {
	var notification struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return
	}
	identity, valid := agentapi.NewControlRequestIdentity(notification.RequestID)
	if !valid {
		return
	}
	b.WithdrawControlRequest(b.sink, identity.Key)
}

// handleOutput dispatches one parsed output line as the providerkit.LineHandler for ReadOutputLoop.
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
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agentapi.MessageContent{Original: line.Raw}, agentapi.SpanInfo{}); err != nil {
			slog.Error("acp persist notification", "agent_id", b.AgentID(), "method", line.Method, "error", err)
		}
	}
}

// PublishTurnActive implements Agent for every ACP provider.
// The base already publishes promptActive through one method, and this method supplies the interface entry.
func (b *Base) PublishTurnActive() agentapi.TurnState {
	b.Mu.Lock()
	active := b.promptActive
	steerable := active && b.steersLocked()
	b.Mu.Unlock()
	b.notePromptActive()
	return agentapi.TurnState{Active: active, Steerable: steerable}
}

// IsCurrentSession reports whether this agent currently serves sessionID.
// During startup, before the agent learns its session, every session counts as current.
//
// Acquire only b.Mu, which protects b.sessionID.
// newSessionLocked writes that field under b.Mu, and WithSessionID reads it under the same lock.
//
// Do not acquire b.sessionMu here.
// The session/new request holds it for the complete round trip, and only the reader goroutine can deliver the response.
// This method runs on that reader goroutine.
// An RLock would block the reader until the round trip ends, while the round trip requires the reader to continue.
// ClearContext would then wait for the full API timeout, and every later notification would wait behind the blocked update.
func (b *Base) IsCurrentSession(sessionID string) bool {
	b.Mu.Lock()
	current := b.sessionID
	b.Mu.Unlock()
	return current == "" || current == sessionID
}
