package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/util/version"
)

// ACP JSON-RPC method name constants shared across all ACP providers.
const (
	acpMethodInitialize               = "initialize"
	acpMethodSessionUpdate            = "session/update"
	acpMethodSessionRequestPermission = "session/request_permission"
	acpMethodSessionCancel            = "session/cancel"
	acpMethodSessionNew               = "session/new"
	acpMethodSessionLoad              = "session/load"
	acpMethodSessionPrompt            = "session/prompt"
	acpMethodSessionSetModel          = "session/set_model"
	acpMethodSessionSetMode           = "session/set_mode"
)

// ACP session update type constants.
const (
	acpUpdateAgentMessageChunk       = "agent_message_chunk"
	acpUpdateAgentThoughtChunk       = "agent_thought_chunk"
	acpUpdateToolCall                = "tool_call"
	acpUpdateToolCallUpdate          = "tool_call_update"
	acpUpdatePlan                    = "plan"
	acpUpdateUsageUpdate             = "usage_update"
	acpUpdateUserMessageChunk        = "user_message_chunk"
	acpUpdateAvailableCommandsUpdate = "available_commands_update"
	acpUpdateCurrentModeUpdate       = "current_mode_update"
	acpUpdateConfigOptionUpdate      = "config_option_update"
)

// acpPendingInput holds a user message queued while a prompt is in flight.
type acpPendingInput struct {
	content     string
	attachments []*leapmuxv1.Attachment
}

// jsonrpcBase extends processBase with JSON-RPC request/response plumbing
// shared by all ACP agents (OpenCodeAgent, CursorCLIAgent) and CodexAgent.
type jsonrpcBase struct {
	processBase
	responseCorrelator[int64]
	nextReqID atomic.Int64

	// Prompt queueing: ACP servers support only one active prompt per session.
	// Messages arriving mid-turn are queued and coalesced into a single prompt
	// once the current turn completes.
	promptActive    bool              // true while a prompt RPC is in flight
	pendingMessages []acpPendingInput // queued messages waiting for current turn

	// promptFunc is set by the concrete agent during construction. It sends
	// a single prompt RPC and blocks until the turn completes (including
	// response handling). Called by runPrompt on a goroutine.
	promptFunc func(content string, attachments []*leapmuxv1.Attachment)
}

// acpModeChannel identifies how an ACP provider maps the configOptions `mode` select
// to its secondary setting, and thereby which provider family it belongs to. The three
// values are mutually exclusive, so the old syncsPermissionMode/syncsPrimaryAgent bool
// pair (which could illegally be set together) collapses to one field. The zero value
// (modeChannelUnmapped) matches the old "neither bool set" default: permission-mode
// family, configOptions `mode` surfaced read-only.
type acpModeChannel int

const (
	// modeChannelUnmapped: the provider tracks a permission mode but does NOT consume
	// the configOptions `mode` select for it -- it drives the mode through the native
	// modes/current_mode_update channel instead. A configOptions `mode` is
	// surfaced read-only as a generic group rather than applied. This is the
	// zero-value default; no provider currently selects it.
	modeChannelUnmapped acpModeChannel = iota
	// modeChannelPermissionMode: the configOptions `mode` select drives the permission
	// mode (Copilot, Goose, Cursor).
	modeChannelPermissionMode
	// modeChannelPrimaryAgent: the configOptions `mode` select drives the primary agent
	// (OpenCode, Kilo). This is also the sole value identifying the primary-agent family.
	modeChannelPrimaryAgent
)

// acpBase extends jsonrpcBase with fields and methods shared by all ACP
// agents (OpenCodeAgent, CopilotCLIAgent, CursorCLIAgent) but not CodexAgent.
type acpBase struct {
	jsonrpcBase
	sink               OutputSink
	extraSessionUpdate acpSessionUpdateHandler // optional provider-specific session update handler
	extraMethod        acpMethodHandler        // optional provider-specific request/notification handler
	// modelIDNormalizer, when set, rewrites model ids parsed from configOptions
	// (e.g. Cursor's auto<->default[] aliasing) before they reach availableModels.
	modelIDNormalizer func(string) string
	// modeChannel selects how the configOptions `mode` select maps to this provider's
	// secondary setting, and thereby which family the provider belongs to. It replaces
	// the old syncsPermissionMode/syncsPrimaryAgent bool pair so the illegal "both set"
	// state is unrepresentable and every family-conditional site reads one field.
	modeChannel acpModeChannel
	// modelFixedAtLaunch marks a provider whose model is selected once at process
	// launch (e.g. Reasonix's `--model` flag) and cannot change over ACP. For such
	// providers a server config_option_update must not overwrite the model, or the
	// stored/broadcast model would drift from what the process is actually running.
	modelFixedAtLaunch bool
	// primaryAgentHiddenFilter, when set, marks primary-agent ids the provider treats
	// as internal pseudo-agents to hide from the picker (OpenCode's
	// compaction/title/summary). Applied wherever the primary-agent list is built.
	primaryAgentHiddenFilter func(string) bool
	reapplySettings          func()                // called by ClearContext after session/new to re-apply model, mode, etc.
	refreshFromSession       func(json.RawMessage) // called by ClearContext after reapplySettings to sync state from the session response
	sessionID                string
	workingDir               string
	model                    string
	permissionMode           string
	currentPrimaryAgent      string
	availableModels          []*leapmuxv1.AvailableModel
	// modelsFieldInfos holds the models reported through the SessionModelState
	// `models` field at the last full session response (handshake or ClearContext).
	// A runtime config_option_update carries only the configOptions `model` select,
	// so applyConfigOptionModelsLocked re-unions these to keep models-field-only
	// entries from vanishing mid-session for providers that split their catalog.
	modelsFieldInfos       []acpModelInfo
	availableModes         []*leapmuxv1.AvailableOption
	availablePrimaryAgents []*leapmuxv1.AvailableOption
	// genericOptionGroups holds the config-option selectors the model and mode
	// channels did not claim -- a future ACP axis (e.g. thought_level) or a custom
	// "_"-prefixed category. They are surfaced read-only: displayed via
	// AvailableOptionGroups() and kept in sync at handshake / runtime / ClearContext,
	// but not writable. nil when the provider emits no unmapped option, which is every
	// provider today, so the surfacing is byte-for-byte invisible until one does.
	genericOptionGroups []*leapmuxv1.AvailableOptionGroup
	// genericOptionValues maps each generic option's id to its current value, so the
	// live selection rides along in ExtraSettings. nil when none; never an empty map
	// (the keep-stored guard in applyGenericConfigOptionsLocked preserves nil).
	genericOptionValues map[string]string
	turnAssistantText   strings.Builder
	// turnThoughtText buffers consecutive agent_thought_chunk notifications
	// so live token-by-token reasoning streams (each delta is its own
	// notification) coalesce into one message instead of one-message-per-token.
	// Flushed when interrupted by any non-thought event (preserves chronology
	// with tool calls) and at end-of-turn.
	turnThoughtText strings.Builder
	// thinkingTokens is the per-phase generated-token estimate driving the
	// thinking-indicator counter. Fed from both the reader and the prompt-response
	// goroutines, so it self-locks; see thinkingTokenEstimator and thinkingResetSink.
	thinkingTokens thinkingTokenEstimator
}

// handleACPPromptResponse extracts accumulated turn text, calls the optional
// prePersist hook, persists the prompt response, and resets the tool-use count.
func (b *acpBase) handleACPPromptResponse(resp json.RawMessage, prePersist func(json.RawMessage)) {
	if resp == nil {
		// A nil result ends the turn via error/abort, NOT the normal path: the
		// persistPromptResponse turn-end below -- which flushes the buffered thought,
		// persists the reply, and emits the result divider the frontend clears on --
		// is skipped. So nothing here commits the in-flight assistant/thought text,
		// and (unlike the normal path) no frontend clear fires for this turn end.
		// Drop the buffered turn state so an aborted turn's text can't leak into the
		// next turn -- both as a stale handoff signal and as text prepended to the
		// next reply -- since ACP has no turn-start reset. Then clear() the estimate:
		// it broadcasts an explicit zero (no result divider does it for us) so the
		// live counter drops now instead of freezing on its last value until the next
		// turn streams.
		b.mu.Lock()
		b.turnAssistantText.Reset()
		b.turnThoughtText.Reset()
		b.turnToolUses = 0
		b.mu.Unlock()
		b.thinkingTokens.clear(b.sink)
		return
	}

	// Flush any buffered thought text before the end-of-turn assistant
	// message and result divider so trailing thoughts persist in their true
	// chronological position rather than after the reply.
	b.flushThoughtBuffer()

	b.mu.Lock()
	assistantText := b.turnAssistantText.String()
	b.turnAssistantText.Reset()
	b.turnToolUses = 0
	b.mu.Unlock()

	if prePersist != nil {
		prePersist(resp)
	}

	b.persistPromptResponse(assistantText, resp, func(resp json.RawMessage) json.RawMessage {
		return b.enrichWithToolUses(resp)
	})
}

// acpSessionUpdateHandler is called for session update types not handled by
// the shared dispatcher. Return true if the update was consumed.
type acpSessionUpdateHandler func(sessionUpdate string, update json.RawMessage) bool

// acpMethodHandler is called for JSON-RPC methods not handled by the shared
// ACP dispatcher. Return true if the method was consumed.
type acpMethodHandler func(line *parsedLine) bool

// handleACPSessionUpdate dispatches ACP sessionUpdate notifications by type.
func (b *acpBase) handleACPSessionUpdate(params json.RawMessage, extra acpSessionUpdateHandler) {
	var wrapper struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("acp session update unmarshal wrapper failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if len(wrapper.Update) == 0 {
		return
	}
	update := wrapper.Update

	var header struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Role          string          `json:"role"`
		Content       json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(update, &header); err != nil {
		slog.Warn("acp session update unmarshal header failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}

	if header.Role == "result" {
		return
	}

	// Flush any buffered thought text before handling an event that
	// produces a visible/persisted message, so coalesced thoughts appear at
	// their real chronological position relative to tool calls and replies.
	// Skip the flush for thought chunks themselves and for purely
	// informational updates (usage, history replay, command list).
	switch header.SessionUpdate {
	case acpUpdateAgentThoughtChunk,
		acpUpdateUsageUpdate,
		acpUpdateUserMessageChunk,
		acpUpdateAvailableCommandsUpdate:
		// no flush
	default:
		b.flushThoughtBuffer()
	}

	switch header.SessionUpdate {
	case acpUpdateAgentMessageChunk:
		b.broadcastACPChunk(header.Content, &b.turnAssistantText, acpUpdateAgentMessageChunk)
	case acpUpdateAgentThoughtChunk:
		b.handleAgentThoughtChunk(header.Content)
	case acpUpdateToolCall:
		b.handleToolCall(update)
	case acpUpdateToolCallUpdate:
		b.handleToolCallUpdate(update)
	case acpUpdatePlan:
		b.handlePlan(update)
	case acpUpdateUsageUpdate:
		b.handleUsageUpdate(update)
	case acpUpdateConfigOptionUpdate:
		// Generic model channel for every ACP provider; mode handled per-provider.
		b.handleACPConfigOptionUpdate(update)
	case acpUpdateUserMessageChunk, acpUpdateAvailableCommandsUpdate:
		// No-op: user_message_chunk is history replay; available_commands_update is informational.
	default:
		if extra != nil && extra(header.SessionUpdate, update) {
			return
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, update, SpanInfo{}); err != nil {
			slog.Error("persist unknown acp sessionUpdate", "agent_id", b.agentID, "type", header.SessionUpdate, "error", err)
		}
	}
}

// ClearContext sends a session/new request on the running ACP process,
// replacing the current session with a fresh one. After the session is
// created, the reapplySettings callback (if set) re-applies provider-
// specific settings such as model and permission mode.
func (b *acpBase) ClearContext() (string, bool) {
	_, params := buildACPSessionRequest("", b.workingDir, acpMethodSessionNew, "")
	resp, err := b.sendRequest(acpMethodSessionNew, json.RawMessage(params), b.APITimeout())
	if err != nil {
		slog.Error("acp ClearContext failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", false
	}
	if err := jsonRPCResultError(resp); err != nil {
		slog.Error("acp ClearContext: RPC error", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", false
	}

	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &session); err != nil || session.SessionID == "" {
		slog.Error("acp ClearContext: invalid response", "provider", b.providerName, "agent_id", b.agentID, "error", err, "response", string(resp))
		return "", false
	}

	b.mu.Lock()
	b.sessionID = session.SessionID
	b.turnAssistantText.Reset()
	b.turnThoughtText.Reset()
	b.mu.Unlock()
	// The session was replaced; drop any in-flight thinking-token estimate too.
	b.thinkingTokens.reset()

	b.sink.UpdateSessionID(session.SessionID)

	if b.reapplySettings != nil {
		b.reapplySettings()
	}
	if b.refreshFromSession != nil {
		b.refreshFromSession(resp)
	}
	return session.SessionID, true
}

// reapplyModelAndSecondary re-applies the current model and one secondary setting
// (permission mode or primary agent) after a session/new. `setModel` lets Cursor
// pass setCursorModel for its wire-format mapping; every other provider passes the
// base setModel. Shared by reapplyModelAndPermissionMode, reapplyModelAndPrimaryAgent,
// and Cursor's reapplyModelAndMode, whose bodies were otherwise identical.
func (b *acpBase) reapplyModelAndSecondary(secondary *string, secondaryLabel string, setModel, setSecondary func(string) error) {
	b.mu.Lock()
	model, sec := b.model, *secondary
	b.mu.Unlock()
	acpApplySetting(b.providerName, b.agentID, "model", model, setModel)
	acpApplySetting(b.providerName, b.agentID, secondaryLabel, sec, setSecondary)
}

// reapplyModelAndPermissionMode re-applies the current model and permission
// mode after a session/new.
func (b *acpBase) reapplyModelAndPermissionMode() {
	b.reapplyModelAndSecondary(&b.permissionMode, "mode", b.setModel, b.setPermissionMode)
}

// setPermissionMode sends a session/set_mode RPC and updates the local field.
func (b *acpBase) setPermissionMode(mode string) error {
	b.mu.Lock()
	available := b.availableModes
	b.mu.Unlock()

	if err := b.acpSetMode(mode, available); err != nil {
		return err
	}
	b.mu.Lock()
	b.permissionMode = mode
	b.mu.Unlock()
	return nil
}

// CurrentSettings dispatches on the provider family: primary-agent providers (OpenCode,
// Kilo) carry the selection in extras, every other ACP provider in the permissionMode
// field. Cursor stores its model normalized, so it needs no override here.
func (b *acpBase) CurrentSettings() *leapmuxv1.AgentSettings {
	if b.modeChannel == modeChannelPrimaryAgent {
		return b.primaryAgentCurrentSettings()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          b.model,
		PermissionMode: b.permissionMode,
		// nil base: the permission mode lives in its own field, not extras. With no
		// generics this stays nil, leaving ExtraSettings byte-for-byte as before.
		ExtraSettings: b.mergeGenericExtrasLocked(nil),
	}
}

// UpdateSettings dispatches on the provider family: primary-agent providers (OpenCode,
// Kilo) write the model and primary agent, every other ACP provider the model and
// permission mode. Cursor overrides this because its model writes go through
// setCursorModel for the wire-format mapping.
func (b *acpBase) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if b.modeChannel == modeChannelPrimaryAgent {
		return b.primaryAgentUpdateSettings(s)
	}
	ok := acpApplySetting(b.providerName, b.agentID, "model", s.GetModel(), b.setModel)
	ok = acpApplySetting(b.providerName, b.agentID, "mode", s.GetPermissionMode(), b.setPermissionMode) && ok
	return ok
}

// Used by agents that track primaryAgent instead of permissionMode
// (Kilo, OpenCode).
func (b *acpBase) setPrimaryAgent(agent string) error {
	b.mu.Lock()
	available := b.availablePrimaryAgents
	b.mu.Unlock()

	if err := b.acpSetMode(agent, available); err != nil {
		return err
	}
	b.mu.Lock()
	b.currentPrimaryAgent = agent
	b.mu.Unlock()
	return nil
}

// reapplyModelAndPrimaryAgent re-applies the current model and primary
// agent after a session/new.
func (b *acpBase) reapplyModelAndPrimaryAgent() {
	b.reapplyModelAndSecondary(&b.currentPrimaryAgent, "primary agent", b.setModel, b.setPrimaryAgent)
}

// applySessionRefresh parses the session response once, refreshes availableModels
// from both model channels, updates b.model (optionally normalized via
// `normalizeModel`) and the field pointed to by `secondary`, then logs and persists.
// `secondaryLogKey` is the slog attribute name for the secondary field (e.g. "mode",
// "primaryAgent"). Both the model list AND the secondary-option list (available modes /
// primary agents) are refreshed -- not just the current ids -- so a ClearContext'd
// session whose available options differ from the original handshake reflects the new
// lists instead of going stale; the current selection is then resolved against the
// refreshed list via reconcileCurrentOptionID. How the secondary value is persisted
// (its own permissionMode arg vs. an extras key) is derived from b.modeChannel, so
// callers no longer pass per-family extras/broadcast callbacks.
func (b *acpBase) applySessionRefresh(
	resp json.RawMessage,
	normalizeModel func(string) string,
	secondary *string,
	secondaryLogKey string,
) {
	// Derive the available-model list (the union of both channels) and the current
	// model/secondary id from a single parse of resp.
	var model, secondaryVal string
	var models []*leapmuxv1.AvailableModel
	var modelsFieldInfos []acpModelInfo
	var modes []acpModeInfo
	var configOptions []acpConfigOption
	if session, err := parseACPSessionResult(resp); err == nil {
		modelsFieldInfos = session.Models
		modes = session.Modes
		configOptions = session.ConfigOptions
		// The current model id is read from the models-field currentModelId, not the
		// union: for config-only providers (OpenCode/Kilo) it is "", which leaves
		// b.model untouched so the model re-pushed by reapplySettings just before this
		// refresh is kept. The list, by contrast, is always rebuilt from both channels.
		model = session.CurrentModelID
		secondaryVal = session.CurrentModeID
		if infos, current := acpHandshakeModelInfos(session); len(infos) > 0 {
			models, _ = b.buildModels(infos, current)
		}
	} else {
		// A malformed session response would otherwise look like a successful no-op
		// refresh (every guard below is skipped, stored settings are kept). Surface it
		// so a genuinely broken ClearContext reply isn't silently swallowed.
		slog.Warn("acp agent session refresh: failed to parse session response, keeping stored settings",
			"provider", b.providerName, "agent_id", b.agentID, "error", err)
	}
	b.mu.Lock()
	// Refresh availableModels and the remembered models-field catalog together so the
	// two never desync. An empty parse (no models in either channel) leaves both at the
	// prior session's values rather than blanking modelsFieldInfos while the stale list
	// lingers -- which would drop models-field-only entries on the next
	// config_option_update re-union.
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
	// Refresh the available secondary-option list from the native modes channel, mirroring
	// the handshake (buildACPModes for the permission-mode/unmapped families,
	// buildPrimaryAgentOptions for the primary-agent family), so a ClearContext refreshes
	// the picker's options instead of freezing them at the handshake value while the model
	// list updates. An empty rebuild keeps the prior list (the new session did not
	// re-report the modes channel). The configOptions override below further refreshes it
	// when a `mode` select is present. [S4]
	if b.modeChannel == modeChannelPrimaryAgent {
		if rebuilt := b.buildPrimaryAgentOptions(modes, secondaryVal); len(rebuilt) > 0 {
			b.availablePrimaryAgents = rebuilt
		}
	} else if rebuilt := buildACPModes(modes, secondaryVal, nil); len(rebuilt) > 0 {
		b.availableModes = rebuilt
	}
	// A primary-agent provider's modes-channel currentModeId can be a hidden pseudo-agent
	// (OpenCode's compaction/title/summary). Drop it before resolving so the empty-list
	// "trust the reported value" path in reconcileCurrentOptionID can't adopt one,
	// mirroring the handshake and runtime guards.
	if b.modeChannel == modeChannelPrimaryAgent && b.primaryAgentHiddenFilter != nil && b.primaryAgentHiddenFilter(secondaryVal) {
		secondaryVal = ""
	}
	// Resolve the current against the refreshed list: adopt a valid reported value, keep the
	// stored selection (re-pushed by reapplySettings just before this) if still selectable,
	// else re-seed to the default-or-first option, so a ClearContext never seeds the picker
	// with a selection absent from the list -- the same resolution the handshake and runtime
	// paths apply. The configOptions override below still wins when a `mode` select resolves
	// to a visible value. [S2]
	available := b.availableModes
	if b.modeChannel == modeChannelPrimaryAgent {
		available = b.availablePrimaryAgents
	}
	if resolved := reconcileCurrentOptionID(available, secondaryVal, *secondary); resolved != "" {
		*secondary = resolved
	}
	// For providers whose configOptions `mode` maps to the permission mode
	// (Copilot/Goose/Cursor), apply that override here too -- matching
	// applyHandshakeMode, so a ClearContext resolves the mode the same way the
	// handshake does instead of taking only the modes-channel value. secondary
	// aliases b.permissionMode for these providers, so snapshotSecondary below
	// reflects the override.
	if b.modeChannel == modeChannelPermissionMode {
		b.syncConfigOptionModeLocked(configOptions)
	}
	// Mirror of the permission-mode block above for the primary-agent providers (OpenCode,
	// Kilo): apply the configOptions primary-agent override on ClearContext, so a `mode`
	// select carried in the session response resolves the primary agent the same way the
	// handshake's configOptions handling and a runtime config_option_update do. The
	// native-modes rebuild and reconcile above already refreshed availablePrimaryAgents and
	// the current, so this is a no-op for OpenCode/Kilo -- whose session responses report the
	// primary agent through the native modes channel, not a configOptions `mode` -- and takes
	// effect only for a session response that does carry one. secondary aliases
	// b.currentPrimaryAgent for these providers, so snapshotSecondary below reflects any override.
	if b.modeChannel == modeChannelPrimaryAgent {
		b.syncConfigOptionPrimaryAgentLocked(configOptions)
	}
	// Refresh the read-only generic groups from the new session, next to the mapped
	// channels above, so a ClearContext resolves them the same way the handshake and a
	// runtime config_option_update do. The keep-stored guard makes this a no-op for the
	// providers that emit no unmapped option (all of them today).
	b.applyGenericConfigOptionsLocked(configOptions)
	snapshotModel := b.model
	snapshotSecondary := *secondary
	// Derive how the secondary value is persisted from the family: the primary-agent
	// providers carry it in extras (under primaryAgentExtras, which the generics overlay
	// onto), the permission-mode providers in PersistSettingsRefresh's own mode arg.
	// Snapshot the generic extras under the SAME lock as model/secondary so a racing
	// runtime config_option_update can't pair a freshly-changed generic value with a
	// stale model/secondary in the persisted row.
	var extrasBase map[string]string
	var persistMode string
	if b.modeChannel == modeChannelPrimaryAgent {
		extrasBase = primaryAgentExtras(snapshotSecondary)
	} else {
		persistMode = snapshotSecondary
	}
	extras := b.mergeGenericExtrasLocked(extrasBase)
	b.mu.Unlock()
	slog.Info("acp agent settings refreshed from session",
		"provider", b.providerName,
		"agent_id", b.agentID,
		"model", snapshotModel,
		secondaryLogKey, snapshotSecondary,
	)
	b.sink.PersistSettingsRefresh(snapshotModel, "", persistMode, extras)
}

// refreshModelAndPermissionModeFromSession is used by agents that track
// permissionMode (Copilot, Goose, Cursor). applySessionRefresh persists the mode in
// PersistSettingsRefresh's own arg and overlays the live generic values, keyed off
// b.modeChannel.
func (b *acpBase) refreshModelAndPermissionModeFromSession(resp json.RawMessage) {
	b.applySessionRefresh(resp, nil, &b.permissionMode, "mode")
}

// refreshModelAndPrimaryAgentFromSession is used by agents that track
// currentPrimaryAgent (OpenCode, Kilo). applySessionRefresh carries the primary agent in
// the extras (under primaryAgentExtras) and overlays the live generic values, keyed off
// b.modeChannel.
func (b *acpBase) refreshModelAndPrimaryAgentFromSession(resp json.RawMessage) {
	b.applySessionRefresh(resp, nil, &b.currentPrimaryAgent, "primaryAgent")
}

// primaryAgentExtras builds the extra-settings map carrying the primary-agent
// selection for OpenCode/Kilo, returning nil (not an empty map) when the agent
// is empty. nil tells PersistSettingsRefresh to keep the stored extras, whereas
// a non-nil map{primaryAgent: ""} would marshal to "{}" (marshalExtraSettings
// drops empty values) and wipe the stored primary agent. Used by every path
// that persists or reports primary-agent extras so they can't diverge.
func primaryAgentExtras(agent string) map[string]string {
	if agent == "" {
		return nil
	}
	return map[string]string{OptionGroupKeyPrimaryAgent: agent}
}

// configurePrimaryAgents sets the available primary agents and current selection
// from the handshake modes channel, falling back to `fallback`/`defaultAgent` when
// the server reports no modes, then applies `requestedPrimaryAgent` if it differs
// from the current and is available. Shared by OpenCode and Kilo, whose
// primary-agent handling is identical apart from the fallback list, default
// constant, and hidden-agent filter (set via primaryAgentHiddenFilter). Returns an
// error only when the requested-agent set_mode RPC fails.
func (b *acpBase) configurePrimaryAgents(modes []acpModeInfo, currentModeID, requestedPrimaryAgent string, fallback []*leapmuxv1.AvailableOption, defaultAgent string) error {
	available := b.buildPrimaryAgentOptions(modes, currentModeID)
	hasACPModeList := len(available) > 0
	current := currentModeID
	if !hasACPModeList {
		available = fallback
		if current == "" {
			current = defaultAgent
		}
	}
	// Resolve the current against the (hidden-filtered) available list: drop a value the
	// list lacks (e.g. a hidden pseudo-agent like OpenCode's "compaction") and re-seed to
	// the default-or-first option, so the picker is never seeded with a selection that has
	// no matching option. There is no prior selection at handshake, so stored is "". The
	// runtime and ClearContext paths share this resolver, so all three seams agree.
	current = reconcileCurrentOptionID(available, current, "")

	b.mu.Lock()
	b.availablePrimaryAgents = available
	b.currentPrimaryAgent = current
	b.mu.Unlock()

	if hasACPModeList && requestedPrimaryAgent != "" && requestedPrimaryAgent != current && hasACPOption(available, requestedPrimaryAgent) {
		if err := b.setPrimaryAgent(requestedPrimaryAgent); err != nil {
			return err
		}
	}

	return nil
}

// buildPrimaryAgentOptions converts the handshake modes channel into primary-agent
// options, normalizing names (OpenCode-family agents often report name == id or
// whitespace-only names) and skipping ids the provider marks hidden via
// primaryAgentHiddenFilter (OpenCode's compaction/title/summary pseudo-agents).
func (b *acpBase) buildPrimaryAgentOptions(modes []acpModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	normalized := make([]acpModeInfo, len(modes))
	copy(normalized, modes)
	for i := range normalized {
		normalized[i].Name = normalizeOptionName(normalized[i].Name, normalized[i].ID)
	}
	return buildACPModes(normalized, currentModeID, b.primaryAgentHiddenFilter)
}

// defaultOrFirstOption returns the default option's id, else the first non-empty id,
// else "". Used by reconcileCurrentOptionID to seed a secondary channel's current
// selection (permission mode or primary agent) when the server reports no valid current.
func defaultOrFirstOption(options []*leapmuxv1.AvailableOption) string {
	for _, option := range options {
		if option != nil && option.IsDefault && option.Id != "" {
			return option.Id
		}
	}
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
	if reported != "" && hasACPOption(available, reported) {
		return reported
	}
	if stored != "" && hasACPOption(available, stored) {
		return stored
	}
	return defaultOrFirstOption(available)
}

// mappedOptionGroupsLocked returns the single mapped (writable) group followed by any
// read-only generic groups in insertion order. With no generics this is byte-for-byte
// the single-group list every provider returned before generic surfacing. Shared by
// permissionModeOptionGroups and primaryAgentOptionGroups so the "mapped group first,
// generics appended" invariant lives in one place. The caller must hold b.mu.
func (b *acpBase) mappedOptionGroupsLocked(key, label string, options []*leapmuxv1.AvailableOption) []*leapmuxv1.AvailableOptionGroup {
	groups := []*leapmuxv1.AvailableOptionGroup{{
		Key:     key,
		Label:   label,
		Options: options,
	}}
	return append(groups, b.genericOptionGroups...)
}

// permissionModeOptionGroups returns AvailableOptionGroups for agents that
// use permission-mode (e.g. Copilot, Cursor, Goose).
func (b *acpBase) permissionModeOptionGroups(label string, fallback []*leapmuxv1.AvailableOption) []*leapmuxv1.AvailableOptionGroup {
	b.mu.Lock()
	defer b.mu.Unlock()
	options := b.availableModes
	if len(options) == 0 {
		options = fallback
	}
	return b.mappedOptionGroupsLocked(OptionGroupKeyPermissionMode, label, options)
}

// primaryAgentOptionGroups returns AvailableOptionGroups for agents that
// use primary-agent selection (e.g. Kilo, OpenCode).
func (b *acpBase) primaryAgentOptionGroups(fallback []*leapmuxv1.AvailableOption) []*leapmuxv1.AvailableOptionGroup {
	b.mu.Lock()
	defer b.mu.Unlock()
	options := b.availablePrimaryAgents
	if len(options) == 0 {
		options = fallback
	}
	return b.mappedOptionGroupsLocked(OptionGroupKeyPrimaryAgent, "Primary Agent", options)
}

// Used by agents that track primaryAgent instead of permissionMode
// (Kilo, OpenCode).
func (b *acpBase) primaryAgentCurrentSettings() *leapmuxv1.AgentSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:         b.model,
		ExtraSettings: b.mergeGenericExtrasLocked(primaryAgentExtras(b.currentPrimaryAgent)),
	}
}

// Used by agents that track primaryAgent instead of permissionMode
// (Kilo, OpenCode).
func (b *acpBase) primaryAgentUpdateSettings(s *leapmuxv1.AgentSettings) bool {
	ok := acpApplySetting(b.providerName, b.agentID, "model", s.GetModel(), b.setModel)
	ok = acpApplySetting(b.providerName, b.agentID, "primary agent", s.GetExtraSettings()[OptionGroupKeyPrimaryAgent], b.setPrimaryAgent) && ok
	return ok
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
// OpenCode, Kilo, Copilot, Goose, and Cursor: protocol version 1, the LeapMux
// clientInfo, and empty capabilities.
func acpStandardInitParams() (json.RawMessage, error) {
	params, err := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	return params, nil
}

// buildACPSessionRequest builds a newSession or loadSession JSON-RPC request.
func buildACPSessionRequest(resumeSessionID, workingDir, newMethod, resumeMethod string) (method string, params []byte) {
	p := map[string]interface{}{
		"cwd":        workingDir,
		"mcpServers": []interface{}{},
	}
	method = newMethod
	if resumeSessionID != "" {
		p["sessionId"] = resumeSessionID
		method = resumeMethod
	}
	params, err := json.Marshal(p)
	if err != nil {
		slog.Warn("acp session request marshal failed", "error", err)
	}
	return method, params
}

// jsonrpcMessage is a typed struct for serializing JSON-RPC requests and
// notifications. ID is omitted for notifications (IDs start at 1 via
// nextReqID.Add(1), so 0 is safely treated as "absent").
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponseMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func (b *jsonrpcBase) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := b.nextReqID.Add(1)

	ch, release := b.register(reqID)
	defer release()

	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      reqID,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if err := b.writeStdin(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	return b.awaitResponse(ch, method, timeout)
}

func (b *jsonrpcBase) sendNotification(method string, params json.RawMessage) error {
	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if err := b.writeStdin(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

func (b *jsonrpcBase) sendResponse(id json.RawMessage, result any) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (b *jsonrpcBase) sendErrorResponse(id json.RawMessage, code int, message string) error {
	return b.writeJSONRPCResponse(jsonrpcResponseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

func (b *jsonrpcBase) writeJSONRPCResponse(resp jsonrpcResponseMessage) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	data = append(data, '\n')
	if err := b.writeStdin(data); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

// handleJSONRPCResponse checks if a parsed line is a JSON-RPC response and
// routes it to the pending request channel. Returns true if the line was consumed.
func (b *jsonrpcBase) handleJSONRPCResponse(line *parsedLine) bool {
	if !line.HasID() || line.Method != "" {
		return false
	}

	reqID, ok := line.IDInt64()
	if !ok {
		return false
	}

	body := line.Result
	if len(line.Error) > 0 && string(line.Error) != "null" {
		body = line.Error
	}
	return b.deliver(reqID, body)
}

// readOutputLoop reads JSONL lines from stdout, using handleJSONRPCResponse as
// the interceptor and forwarding remaining lines to the given output handler.
func (b *jsonrpcBase) readOutputLoop(scanner *bufio.Scanner, handle outputHandler) {
	b.readOutput(scanner, b.handleJSONRPCResponse, handle)
}

// enqueueOrSendPrompt queues a message if a prompt is in flight, or starts
// a new prompt goroutine if idle. Returns an error only if the agent is stopped.
func (b *jsonrpcBase) enqueueOrSendPrompt(content string, attachments []*leapmuxv1.Attachment) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if b.promptActive {
		b.pendingMessages = append(b.pendingMessages, acpPendingInput{content, attachments})
		b.mu.Unlock()
		return nil
	}
	b.promptActive = true
	b.mu.Unlock()

	go b.runPrompt(content, attachments)
	return nil
}

// runPrompt calls the agent's promptFunc and then drains any queued messages.
// It runs on a dedicated goroutine and loops until the queue is empty.
func (b *jsonrpcBase) runPrompt(content string, attachments []*leapmuxv1.Attachment) {
	for {
		b.promptFunc(content, attachments)

		b.mu.Lock()
		if len(b.pendingMessages) == 0 || b.stopped {
			b.promptActive = false
			b.pendingMessages = nil
			b.mu.Unlock()
			return
		}
		pending := b.pendingMessages
		b.pendingMessages = nil
		b.mu.Unlock()

		// Coalesce queued messages into a single prompt.
		var parts []string
		var allAttachments []*leapmuxv1.Attachment
		for _, p := range pending {
			if p.content != "" {
				parts = append(parts, p.content)
			}
			allAttachments = append(allAttachments, p.attachments...)
		}
		content = strings.Join(parts, "\n\n")
		attachments = allAttachments
	}
}

// SendInput queues a user message or starts a new prompt if idle.
func (b *acpBase) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	b.mu.Lock()
	if b.sessionID == "" {
		b.mu.Unlock()
		return fmt.Errorf("agent has no active session")
	}
	b.mu.Unlock()
	return b.enqueueOrSendPrompt(content, attachments)
}

// Stop clears the prompt queue and terminates the process.
func (b *acpBase) Stop() {
	b.clearPromptQueue()
	b.processBase.Stop()
}

// stopAndWait stops the agent process and blocks until it exits. Used by the
// handshake and every Start* path to tear down a half-initialized agent on a
// fatal startup error.
func (b *acpBase) stopAndWait() {
	b.Stop()
	_ = b.Wait()
}

// clearPromptQueue discards any queued messages and resets the prompt-active flag.
func (b *jsonrpcBase) clearPromptQueue() {
	b.mu.Lock()
	b.pendingMessages = nil
	b.promptActive = false
	b.mu.Unlock()
}

// sendPrompt builds and sends an ACP prompt, then calls the provided
// response handler. Shared by all ACP agent doSendPrompt implementations.
func (b *acpBase) sendPrompt(
	content string,
	attachments []*leapmuxv1.Attachment,
	sendRPC func(json.RawMessage) (json.RawMessage, error),
	handleResponse func(json.RawMessage),
) {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	prompt := buildACPPromptBlocks(content, classifyAttachments(attachments))
	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	})
	if err != nil {
		slog.Warn("acp marshal prompt params", "agent_id", b.agentID, "error", err)
		return
	}

	resp, err := sendRPC(json.RawMessage(params))
	if err != nil {
		if !b.IsStopped() {
			slog.Error("acp prompt failed", "agent_id", b.agentID, "error", err)
			b.sink.PersistLeapMuxNotification(map[string]interface{}{
				"type":  NotificationTypeAgentError,
				"error": fmt.Sprintf("prompt failed: %v", err),
			})
		}
		return
	}
	handleResponse(resp)
}

// extractACPChunkText pulls the `text` field from an ACP content envelope.
// Returns "" when the field is absent, empty, or the unmarshal fails (a warning
// is logged in the failure case). The `kind` argument labels the warning so
// failures can be attributed to a specific session update type.
func (b *acpBase) extractACPChunkText(content json.RawMessage, kind string) string {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &c); err != nil {
		slog.Warn("acp content unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "kind", kind, "error", err)
		return ""
	}
	return c.Text
}

// broadcastACPChunk extracts text from a pre-parsed content RawMessage and
// broadcasts it. The content JSON is already extracted from the header parse,
// avoiding a full re-unmarshal of the update on the streaming hot path.
func (b *acpBase) broadcastACPChunk(content json.RawMessage, builder *strings.Builder, eventType string) {
	text := b.extractACPChunkText(content, eventType)
	if text == "" {
		return
	}
	b.mu.Lock()
	builder.WriteString(text)
	b.mu.Unlock()
	b.sink.BroadcastStreamChunk([]byte(text), "", eventType)
	b.thinkingTokens.observe(b.sink, text)
}

// handleAgentThoughtChunk buffers an agent_thought_chunk notification's text
// for later flushing. ACP providers vary in chunk granularity — OpenCode for
// instance emits one notification per reasoning *delta* token in live
// streaming (line 513 of opencode/src/acp/agent.ts) but one notification per
// complete reasoning *part* on session replay (line 1087). Persisting each
// notification as its own message produces a separate "Thinking" box per
// token in the live case. We coalesce instead, flushing the buffer when a
// non-thought event interrupts (preserves chronology with tool calls) or at
// end-of-turn.
func (b *acpBase) handleAgentThoughtChunk(content json.RawMessage) {
	text := b.extractACPChunkText(content, acpUpdateAgentThoughtChunk)
	if text == "" {
		return
	}
	b.mu.Lock()
	// freshSegment is true when this chunk opens a new reasoning segment: the
	// thought buffer was empty before it (live thought deltas keep appending to a
	// non-empty buffer; a flush between segments empties it).
	freshSegment := b.turnThoughtText.Len() == 0
	if !freshSegment {
		if sep := thoughtChunkSeparator(b.turnThoughtText.String(), text); sep != "" {
			b.turnThoughtText.WriteString(sep)
		}
	}
	b.turnThoughtText.WriteString(text)
	b.mu.Unlock()
	// A reasoning segment that opens while the live counter still holds uncommitted
	// assistant chars is a new phase: the assistant chunks were only buffered in
	// turnAssistantText and not committed until turn end, so they triggered no
	// frontend clear. clear() supplies one so the reasoning reads as its own phase
	// rather than inheriting the assistant total (which would also spin the
	// forward-only odometer backward from the larger assistant count to the smaller
	// reasoning count). Gating on the estimator's own pending chars -- not on
	// turnAssistantText, which stays non-empty all turn for end-of-turn persistence
	// -- keeps a committed tool call between assistant text and a later reasoning
	// segment from re-firing a redundant clear: that commit already reset the
	// estimator and cleared the frontend, so hasPending then reports false.
	if freshSegment && b.thinkingTokens.hasPending() {
		b.thinkingTokens.clear(b.sink)
	}
	// Reasoning is model generation too: feed it into the live token estimate.
	b.thinkingTokens.observe(b.sink, text)
}

// flushThoughtBuffer persists the buffered thought text (if any) as one
// agent_thought_chunk message and resets the buffer.
func (b *acpBase) flushThoughtBuffer() {
	b.mu.Lock()
	text := b.turnThoughtText.String()
	b.turnThoughtText.Reset()
	b.mu.Unlock()
	if text == "" {
		return
	}
	b.persistTextMessage(acpUpdateAgentThoughtChunk, text)
}

// thoughtChunkSeparator returns the string (if any) that should be inserted
// between an existing buffer and the next chunk to keep paragraph structure
// intact at chunk seams.
//
// The wire format does not expose reasoning-part boundaries (the ACP
// `agent_thought_chunk` notification only carries `messageId`, never the
// underlying part ID). When the boundary lines up with a chunk seam — most
// commonly on session replay, where each complete reasoning part arrives as
// one notification — naive concatenation glues "previous sentence." onto
// "**Next title**" or "feedback." onto "The proposed". We detect that with
// two complementary heuristics:
//
//  1. The new chunk's first non-empty line is a markdown bold heading
//     (^\*\*[^*]+\*\*$) or ATX heading (^#{1,6} ).
//  2. The seam glues sentence-ending punctuation (.?!) directly onto a
//     capital letter or `**`, with no whitespace on either side.
//
// Live token-by-token deltas usually have whitespace on at least one side of
// the seam (or split mid-word), so they don't trigger either heuristic.
func thoughtChunkSeparator(buffer, chunk string) string {
	if buffer == "" || chunk == "" {
		return ""
	}
	// Already separated by paragraph break across the seam.
	trailingNL := countTrailing(buffer, '\n')
	leadingNL := countLeading(chunk, '\n')
	if trailingNL+leadingNL >= 2 {
		return ""
	}

	if looksLikeMarkdownHeading(firstNonEmptyLine(chunk)) {
		// Pad with whatever newlines the seam doesn't already provide.
		return strings.Repeat("\n", 2-(trailingNL+leadingNL))
	}

	// Sentence-end glued to capital letter or bold marker, with no
	// whitespace at the seam. e.g. "...feedback." + "The proposed..."
	if trailingNL == 0 && leadingNL == 0 {
		bufLast := lastRune(buffer)
		chunkFirst := firstRune(chunk)
		if isSentenceEnd(bufLast) && (unicode.IsUpper(chunkFirst) || strings.HasPrefix(chunk, "**")) &&
			!unicode.IsSpace(bufLast) && !unicode.IsSpace(chunkFirst) {
			return "\n\n"
		}
	}
	return ""
}

func countTrailing(s string, r byte) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == r; i-- {
		n++
	}
	return n
}

func countLeading(s string, r byte) int {
	n := 0
	for i := 0; i < len(s) && s[i] == r; i++ {
		n++
	}
	return n
}

func firstNonEmptyLine(s string) string {
	for {
		nl := strings.IndexByte(s, '\n')
		var line string
		if nl < 0 {
			line = s
			s = ""
		} else {
			line = s[:nl]
			s = s[nl+1:]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
		if s == "" {
			return ""
		}
	}
}

// looksLikeMarkdownHeading recognises a line that is either a fully wrapped
// markdown bold heading (`**Title**`) or an ATX heading (`# Title`).
// Inline bold within prose (a delta like just `**` or `**word`) does not
// match because the line must both start AND end with `**`.
func looksLikeMarkdownHeading(line string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) >= 5 {
		inner := line[2 : len(line)-2]
		if strings.TrimSpace(inner) != "" && !strings.Contains(inner, "**") {
			return true
		}
	}
	if strings.HasPrefix(line, "#") {
		hashes := 0
		for hashes < len(line) && line[hashes] == '#' {
			hashes++
		}
		if hashes >= 1 && hashes <= 6 && hashes < len(line) && line[hashes] == ' ' {
			return true
		}
	}
	return false
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?':
		return true
	}
	return false
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}

func (b *acpBase) persistTextMessage(sessionUpdate, text string) {
	if text == "" {
		return
	}

	msgContent, err := json.Marshal(map[string]interface{}{
		"sessionUpdate": sessionUpdate,
		"content": map[string]interface{}{
			"type": "text",
			"text": text,
		},
	})
	if err != nil {
		slog.Warn("marshal acp text content", "agent_id", b.agentID, "error", err)
		return
	}
	if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msgContent, SpanInfo{}); err != nil {
		slog.Error("persist acp text", "agent_id", b.agentID, "session_update", sessionUpdate, "error", err)
	}
}

func (b *acpBase) persistPromptResponse(
	assistantText string,
	resp json.RawMessage,
	enrich func(json.RawMessage) json.RawMessage,
) {
	b.persistTextMessage(acpUpdateAgentMessageChunk, assistantText)

	resp = unwrapACPResult(resp)
	if enrich != nil {
		resp = enrich(resp)
	}
	if err := b.sink.PersistTurnEnd(resp, SpanInfo{}); err != nil {
		slog.Error("persist acp prompt result", "agent_id", b.agentID, "error", err)
	}
	b.sink.ResetSpans()
}

// unwrapACPResult extracts the inner content from an ACP result message.
// Some ACP server versions return session/prompt results wrapped in:
//
//	{id, role: "result", seq, created_at, content: {stopReason, usage, ...}}
//
// The frontend classifier expects stopReason at the top level, so we unwrap
// the content field. This is a no-op when the response is not wrapped.
func unwrapACPResult(resp json.RawMessage) json.RawMessage {
	var wrapper struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(resp, &wrapper); err != nil {
		slog.Warn("acp unwrap result unmarshal failed", "error", err)
		return resp
	}
	if wrapper.Role != "result" || len(wrapper.Content) == 0 {
		return resp
	}
	return wrapper.Content
}

func (b *acpBase) handleToolCall(update json.RawMessage) {
	var tc struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(update, &tc); err != nil {
		slog.Warn("acp tool_call unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if tc.ToolCallID == "" {
		return
	}

	spanType := tc.Kind
	if spanType == "" {
		spanType = acpUpdateToolCall
	}

	// Tool calls that arrive already terminal (completed/failed/cancelled)
	// are persisted as closing spans immediately — no open/close cycle.
	if tc.Status == "completed" || tc.Status == "failed" || tc.Status == "cancelled" {
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, update, SpanInfo{
			SpanID: tc.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist terminal acp tool_call", "agent_id", b.agentID, "kind", tc.Kind, "status", tc.Status, "error", err)
		}
		return
	}

	spanColor := b.sink.ReserveSpanColor(tc.ToolCallID, "")
	if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, update, SpanInfo{
		SpanID: tc.ToolCallID, SpanType: spanType, SpanColor: spanColor,
	}); err != nil {
		slog.Error("persist acp tool_call", "agent_id", b.agentID, "kind", tc.Kind, "error", err)
	}
	b.sink.SetSpanType(tc.ToolCallID, spanType)
	b.sink.OpenSpan(tc.ToolCallID, "")
}

func (b *acpBase) handleToolCallUpdate(update json.RawMessage) {
	var tcu struct {
		ToolCallID string             `json:"toolCallId"`
		Status     string             `json:"status"`
		Content    []acpToolCallBlock `json:"content"`
	}
	if err := json.Unmarshal(update, &tcu); err != nil {
		slog.Warn("acp tool_call_update unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if tcu.ToolCallID == "" {
		return
	}

	switch tcu.Status {
	case "in_progress":
		// ACP tool_call_update content (when the server provides it) is
		// cumulative; ship only the new tail so the frontend's command-stream
		// buffer doesn't grow quadratically as updates arrive. When the
		// payload carries no text content, drop the broadcast entirely —
		// in_progress with only status fields has nothing useful to stream.
		full := acpToolCallText(tcu.Content)
		if full == "" {
			return
		}
		delta := b.recordCumulativeDelta(tcu.ToolCallID, full)
		if delta == "" {
			return
		}
		b.sink.BroadcastStreamChunk([]byte(delta), tcu.ToolCallID, acpUpdateToolCallUpdate)
	case "completed", "failed", "cancelled":
		b.mu.Lock()
		b.turnToolUses++
		b.mu.Unlock()
		b.clearCumulativeDelta(tcu.ToolCallID)

		spanType := b.sink.GetSpanType(tcu.ToolCallID)
		if spanType == "" {
			spanType = acpUpdateToolCall
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, update, SpanInfo{
			SpanID: tcu.ToolCallID, SpanType: spanType, Closing: true,
		}); err != nil {
			slog.Error("persist acp tool_call_update", "agent_id", b.agentID, "status", tcu.Status, "error", err)
		}
		b.sink.BroadcastStreamEnd(tcu.ToolCallID)
		b.sink.CloseSpan(tcu.ToolCallID)
	}
}

// acpToolCallBlock is the {type, content:{type,text}} shape ACP servers ship
// for tool_call_update.content[]. The outer type is typically "content" and
// the inner content carries the renderable payload.
type acpToolCallBlock struct {
	Type    string `json:"type"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// acpToolCallText concatenates the text from all `{type:"content"}` blocks
// whose inner content is `{type:"text", text:...}`. Returns "" when the
// payload carries no text (e.g. status-only in_progress updates, image-only
// content, or unrecognized block types).
func acpToolCallText(blocks []acpToolCallBlock) string {
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

func (b *acpBase) handleUsageUpdate(update json.RawMessage) {
	var usage struct {
		Used int64 `json:"used"`
		Size int64 `json:"size"`
		Cost struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(update, &usage); err != nil {
		slog.Warn("acp usage update unmarshal failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}

	info := map[string]interface{}{
		"context_usage": map[string]interface{}{
			"input_tokens":                usage.Used,
			"cache_creation_input_tokens": int64(0),
			"cache_read_input_tokens":     int64(0),
			"output_tokens":               int64(0),
			"context_window":              usage.Size,
		},
	}
	if usage.Cost.Amount > 0 {
		info["total_cost_usd"] = usage.Cost.Amount
	}
	b.sink.BroadcastSessionInfo(info)
}

// parseJSONRPCError extracts the code and message from a JSON-RPC error
// response. Returns ok=false if resp is empty, null, or not an error object.
func parseJSONRPCError(resp json.RawMessage) (code int, message string, ok bool) {
	if len(resp) == 0 || string(resp) == "null" {
		return 0, "", false
	}
	var rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return 0, "", false
	}
	if rpcErr.Message == "" {
		return 0, "", false
	}
	return rpcErr.Code, rpcErr.Message, true
}

// jsonRPCResultError returns an error if resp is a JSON-RPC error response.
func jsonRPCResultError(resp json.RawMessage) error {
	code, message, ok := parseJSONRPCError(resp)
	if !ok {
		return nil
	}
	return fmt.Errorf("json-rpc error %d: %s", code, message)
}

// ExtractJSONRPCID extracts the JSON-RPC "id" field from a raw JSON payload,
// returning the raw bytes, its string representation, and whether extraction succeeded.
func ExtractJSONRPCID(content []byte) (json.RawMessage, string, bool) {
	var payload struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(content, &payload); err != nil {
		slog.Warn("json-rpc id unmarshal failed", "error", err)
		return nil, "", false
	}
	if len(payload.ID) == 0 || string(payload.ID) == "null" {
		return nil, "", false
	}

	var text string
	if json.Unmarshal(payload.ID, &text) == nil {
		return payload.ID, text, true
	}

	text = strings.TrimSpace(string(payload.ID))
	if text == "" {
		return nil, "", false
	}
	return payload.ID, text, true
}

// acpSessionConfig describes the session methods for a specific ACP provider.
type acpSessionConfig struct {
	newMethod    string // e.g. "session/new"
	resumeMethod string // e.g. "session/load" or "session/resume"
}

// acpDefaultSessionConfig is the standard ACP session config used by most providers.
var acpDefaultSessionConfig = acpSessionConfig{
	newMethod:    acpMethodSessionNew,
	resumeMethod: acpMethodSessionLoad,
}

// acpSessionResult holds the parsed result of the ACP session handshake.
type acpSessionResult struct {
	SessionID      string
	CurrentModelID string
	Models         []acpModelInfo
	CurrentModeID  string
	Modes          []acpModeInfo
	ConfigOptions  []acpConfigOption
	Raw            json.RawMessage // full session response for provider-specific parsing
}

// startACPHandshake performs the common ACP startup handshake: stderr drain,
// scanner setup, initialize request, session request with resume-fallback,
// session ID validation, and UpdateSessionID/BroadcastStatusActive.
func (b *acpBase) startACPHandshake(
	stdout, stderr io.ReadCloser,
	opts Options,
	initParams json.RawMessage,
	sessionCfg acpSessionConfig,
) (*acpSessionResult, error) {
	b.drainStderr(stderr)

	// Install the thinking-token reset decorator once, centrally, so every ACP
	// provider (Cursor, Copilot, Kilo, OpenCode, Goose) gets the per-phase
	// reset without each Start* constructor remembering to wrap -- and before the
	// reader goroutine below drives any persist/broadcast through b.sink. Codex and
	// Pi wrap in their own constructors; they do not run this handshake.
	b.sink = newThinkingResetSink(b.sink, &b.thinkingTokens)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go b.readOutputLoop(scanner, b.handleOutput)

	cleanup := b.stopAndWait

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	initResp, err := b.sendRequest(acpMethodInitialize, initParams, timeout)
	if err != nil {
		cleanup()
		return nil, b.formatStartupError("initialize", err)
	}
	if err := jsonRPCResultError(initResp); err != nil {
		cleanup()
		return nil, b.formatStartupError("initialize", err)
	}

	// 2. Send session request (resume or new).
	sessionMethod, sessionParams := buildACPSessionRequest(opts.ResumeSessionID, opts.WorkingDir, sessionCfg.newMethod, sessionCfg.resumeMethod)
	sessionResp, err := b.sendRequest(sessionMethod, json.RawMessage(sessionParams), timeout)
	if err != nil {
		if opts.ResumeSessionID != "" {
			slog.Warn("session resume failed, falling back to new session",
				"agent_id", b.agentID, "session_id", opts.ResumeSessionID, "error", err)
			_, fallbackParams := buildACPSessionRequest("", opts.WorkingDir, sessionCfg.newMethod, sessionCfg.resumeMethod)
			sessionResp, err = b.sendRequest(sessionCfg.newMethod, json.RawMessage(fallbackParams), timeout)
		}
		if err != nil {
			cleanup()
			return nil, b.formatStartupError(sessionMethod, err)
		}
	}
	if err := jsonRPCResultError(sessionResp); err != nil {
		cleanup()
		return nil, b.formatStartupError(sessionMethod, err)
	}

	// 3. Parse the common session fields.
	session, err := parseACPSessionResult(sessionResp)
	if err != nil {
		cleanup()
		return nil, b.formatStartupError("session parse", err)
	}
	if session.SessionID == "" && opts.ResumeSessionID != "" && sessionMethod == sessionCfg.resumeMethod {
		session.SessionID = opts.ResumeSessionID
	}
	if session.SessionID == "" {
		cleanup()
		return nil, b.formatStartupError(sessionMethod, fmt.Errorf("response did not contain a session ID"))
	}

	// Write under the lock: the reader goroutine started above before Start*
	// reaches here, and a server pushing a config_option_update right after
	// session/new reads b.sessionID under b.mu (handleACPConfigOptionUpdate's
	// list-change broadcast). This mirrors applyHandshakeModels/applyHandshakeMode,
	// which already write their shared fields under the lock.
	b.mu.Lock()
	b.sessionID = session.SessionID
	b.workingDir = opts.WorkingDir
	sessionID := b.sessionID
	b.mu.Unlock()
	b.sink.UpdateSessionID(sessionID)
	b.sink.BroadcastStatusActive(sessionID)

	return session, nil
}

// acpStartSpec configures acpStart for one ACP provider. acpStart runs the
// fixed launch + handshake pipeline shared by every ACP agent; the spec
// supplies only what differs between providers.
type acpStartSpec[T any] struct {
	providerName   string                                     // process/log name, e.g. "cursor"
	binaryName     string                                     // CLI binary to launch
	baseArgs       []string                                   // args after the binary, e.g. {"acp"}
	rcMarkerEnvKey string                                     // provider rc marker stripped + re-added on a login shell (e.g. "KILO_CLIENT"); "" if none
	sessionConfig  acpSessionConfig                           // zero value -> acpDefaultSessionConfig
	newAgent       func() *T                                  // construct a zero-value concrete agent
	base           func(*T) *acpBase                          // accessor for the agent's embedded acpBase
	configure      func(*T)                                   // set provider-specific hooks before the process starts
	afterHandshake func(*T, *acpSessionResult, Options) error // post-handshake apply step; nil for none
}

// acpStart launches an ACP agent subprocess and performs the initialize +
// session handshake, centralizing the boilerplate shared by every ACP provider
// (Cursor, Copilot, Goose, Kilo, OpenCode, Reasonix). Providers differ only in
// the acpStartSpec fields: the binary/args, an optional rc marker, the session
// config, the hooks set in configure, and the post-handshake apply step.
func acpStart[T any](ctx context.Context, opts Options, sink OutputSink, spec acpStartSpec[T]) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	wrap := shellWrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		BinaryName: spec.binaryName,
		BaseArgs:   spec.baseArgs,
		WorkingDir: opts.WorkingDir,
	}
	if spec.rcMarkerEnvKey != "" {
		wrap.StripEnvKeys = []string{spec.rcMarkerEnvKey}
	}
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, wrap)

	// A provider rc marker is stripped from the inherited env and re-added only for
	// a login shell, so the child detects "launched by leapmux" without inheriting a
	// stale parent value.
	if spec.rcMarkerEnvKey != "" {
		cmd.Env = envutil.FilterEnv(cmd.Environ(), spec.rcMarkerEnvKey)
		if opts.LoginShell {
			cmd.Env = append(cmd.Env, spec.rcMarkerEnvKey+"=1")
		}
		cmd.Env = FinalizeAgentEnv(cmd.Env, opts)
	} else {
		cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)
	}

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := spec.newAgent()
	b := spec.base(a)
	// newProcessBase returns a fresh value (copylocks-exempt: the RHS is a call),
	// so assigning it to the embedded processBase doesn't copy a held lock.
	b.processBase = newProcessBase(opts, spec.providerName, cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)
	b.sink = sink
	b.model = opts.Model
	// Default prompt sender shared by every ACP provider; a provider may override
	// it in configure.
	b.promptFunc = func(content string, attachments []*leapmuxv1.Attachment) {
		b.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
			b.handleACPPromptResponse(resp, nil)
		})
	}
	if spec.configure != nil {
		spec.configure(a)
	}

	if err := b.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := acpStandardInitParams()
	if err != nil {
		return nil, err
	}
	sessionCfg := spec.sessionConfig
	if sessionCfg.newMethod == "" {
		sessionCfg = acpDefaultSessionConfig
	}
	handshake, err := b.startACPHandshake(stdout, stderrPipe, opts, initParams, sessionCfg)
	if err != nil {
		return nil, err
	}

	if spec.afterHandshake != nil {
		if err := spec.afterHandshake(a, handshake, opts); err != nil {
			return nil, err
		}
	}
	// Every concrete ACP agent (*T) implements Agent via its embedded acpBase
	// plus its own overrides; assert it here so acpStart can stay generic over T.
	agent, ok := any(a).(Agent)
	if !ok {
		return nil, fmt.Errorf("acp agent %T does not implement Agent", a)
	}
	return agent, nil
}

// AvailableOptionGroups is the default for ACP providers that surface no mapped
// option group of their own (e.g. Reasonix, which is model-only): it returns
// just the read-only generic groups the server surfaced, if any. Providers with
// a permission-mode or primary-agent group override this method.
func (b *acpBase) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.genericOptionGroups
}

// parseACPSessionResult parses the model/mode/configOptions channels shared by
// every ACP session response -- the handshake (session/new, resume) and the
// ClearContext reply alike. Session-id validation is left to the caller.
func parseACPSessionResult(resp json.RawMessage) (*acpSessionResult, error) {
	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string         `json:"currentModelId"`
			AvailableModels []acpModelInfo `json:"availableModels"`
		} `json:"models"`
		Modes *struct {
			CurrentModeID  string        `json:"currentModeId"`
			AvailableModes []acpModeInfo `json:"availableModes"`
		} `json:"modes"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(resp, &session); err != nil {
		return nil, err
	}

	result := &acpSessionResult{
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

// acpModeInfo is the JSON shape shared by all ACP providers for mode metadata.
type acpModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpModelInfo is the JSON shape shared by all ACP providers for model metadata.
type acpModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type acpConfigOption struct {
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
	Type         string                 `json:"type"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	CurrentValue string                 `json:"currentValue"`
	Options      []acpConfigOptionValue `json:"options"`
}

type acpConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// buildACPModels converts a list of acpModelInfo into proto AvailableModel messages.
// If normalize is non-nil, it is applied to each model ID (and the currentModelID) before use.
//
// Models are deduped by their final (post-normalize) id, keeping the first
// occurrence. This matters because acpHandshakeModelInfos unions two channels and
// dedups by *raw* id: a normalizer that collapses two distinct wire ids to one
// (e.g. Cursor's "default[]" -> "auto") would otherwise emit the same model twice,
// and it also guards against a server repeating an id within a single channel.
func buildACPModels(models []acpModelInfo, currentModelID string, normalize func(string) string) []*leapmuxv1.AvailableModel {
	if normalize != nil {
		currentModelID = normalize(currentModelID)
	}
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
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
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          id,
			DisplayName: name,
			Description: m.Description,
			IsDefault:   id == currentModelID,
		})
	}
	return result
}

// acpConfigOptionIDModel and acpConfigOptionIDMode are the well-known ids ACP
// servers use for the model and mode entries inside a session's configOptions.
const (
	acpConfigOptionIDModel = "model"
	acpConfigOptionIDMode  = "mode"
)

// acpConfigOptionCategoryModel and acpConfigOptionCategoryMode are the ACP spec's
// reserved `category` values for the model and mode selectors. category is the
// semantic signal we dispatch on; the well-known id is only a back-compat fallback.
const (
	acpConfigOptionCategoryModel = "model"
	acpConfigOptionCategoryMode  = "mode"
)

// isSelectableConfigOption reports whether a config option is a value-list selector
// we can render. The ACP spec defines `type` as "select"-only today; an empty type
// is treated as "select" for back-compat (every provider we ship omits it). Any
// other (future) type is ignored defensively so an unknown widget kind never reaches
// a model/mode/generic picker that only understands a list of values.
func isSelectableConfigOption(o acpConfigOption) bool {
	return o.Type == "" || o.Type == "select"
}

// acpConfigOptionByID returns the config option with the given id and true, or a
// zero option and false when none is present.
func acpConfigOptionByID(options []acpConfigOption, id string) (acpConfigOption, bool) {
	for _, option := range options {
		if option.ID == id {
			return option, true
		}
	}
	return acpConfigOption{}, false
}

// acpConfigOptionByCategory returns the selectable config option for a semantic
// category, in two passes: first the option whose `category` matches (the ACP
// spec's intended signal), then -- for the providers we ship today, which omit
// `category` and use the literal well-known id -- the option whose `id` matches
// `fallbackID`. Both passes skip non-selectable options (see
// isSelectableConfigOption), so an unknown widget type is ignored rather than
// dispatched as a model/mode. Returns the matched option and true, or a zero option
// and false when neither pass finds a selectable match.
func acpConfigOptionByCategory(options []acpConfigOption, category, fallbackID string) (acpConfigOption, bool) {
	for _, option := range options {
		if option.Category == category && isSelectableConfigOption(option) {
			return option, true
		}
	}
	if option, ok := acpConfigOptionByID(options, fallbackID); ok && isSelectableConfigOption(option) {
		return option, true
	}
	return acpConfigOption{}, false
}

// acpModelInfosFromConfigOption converts a `model` select config option into the
// common acpModelInfo shape plus its current value, so callers can feed it
// through buildACPModels exactly like the SessionModelState `models` field.
func acpModelInfosFromConfigOption(option acpConfigOption) ([]acpModelInfo, string) {
	infos := make([]acpModelInfo, 0, len(option.Options))
	for _, candidate := range option.Options {
		if candidate.Value == "" {
			continue
		}
		infos = append(infos, acpModelInfo{
			ModelID:     candidate.Value,
			Name:        candidate.Name,
			Description: candidate.Description,
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
func acpHandshakeModelInfos(handshake *acpSessionResult) ([]acpModelInfo, string) {
	infos := handshake.Models
	current := handshake.CurrentModelID

	if option, ok := acpConfigOptionByCategory(handshake.ConfigOptions, acpConfigOptionCategoryModel, acpConfigOptionIDModel); ok {
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
func mergeModelInfos(primary, secondary []acpModelInfo) []acpModelInfo {
	if len(primary) == 0 {
		return secondary
	}
	merged := append([]acpModelInfo(nil), primary...)
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
func (b *acpBase) buildModels(infos []acpModelInfo, currentModelID string) ([]*leapmuxv1.AvailableModel, string) {
	models := buildACPModels(infos, currentModelID, b.modelIDNormalizer)
	if b.modelIDNormalizer != nil {
		currentModelID = b.modelIDNormalizer(currentModelID)
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
// than preserved from the requested opts.Model. That is deliberate: it lets
// trySetStartupModel compare the requested model against the server's actual
// current and push it via setModel when they differ (including when the server
// reports no model at all, the case for agents that accept arbitrary ids without
// advertising a list). Used by every ACP provider's handshake.
func (b *acpBase) applyHandshakeModels(handshake *acpSessionResult) {
	infos, current := acpHandshakeModelInfos(handshake)
	models, current := b.buildModels(infos, current)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.availableModels = models
	b.model = current
	// Remember the models-field catalog so a later config_option_update can
	// re-union it (see applyConfigOptionModelsLocked).
	b.modelsFieldInfos = handshake.Models
	// Surface any config option the model/mode channels did not claim. This is the
	// one universal handshake step every ACP provider runs, so it is the single seam
	// for generic surfacing -- and it is already under the lock.
	b.applyGenericConfigOptionsLocked(handshake.ConfigOptions)
}

// trySetStartupModel applies a requested model during startup, best-effort. It is
// a no-op when the request is empty or already matches the server's current
// model. A failure is logged but NON-FATAL: the agent keeps the server's current
// model and stays usable. This is intentional -- some ACP agents do not advertise
// a model list and accept arbitrary ids, so a rejected model must not abort an
// otherwise-healthy session whose other settings were already applied.
func (b *acpBase) trySetStartupModel(requested string, set func(string) error) {
	if requested == "" {
		return
	}
	b.mu.Lock()
	current := b.model
	b.mu.Unlock()
	if requested == current {
		return
	}
	if err := set(requested); err != nil {
		slog.Warn("requested model not applied; keeping current model",
			"provider", b.providerName, "agent_id", b.agentID,
			"requested", requested, "current", current, "error", err)
	}
}

// applyStartupPermissionMode applies a requested permission mode during startup
// for providers that track one (Copilot, Goose, Cursor). It is a no-op
// when the request is empty or already matches the server's current mode. Unlike
// the model, the mode is mandatory: a rejected mode returns an error so the caller
// aborts startup.
//
// The current mode is read under b.mu: startACPHandshake starts the reader
// goroutine before Start* reaches this point, and that goroutine can write
// permissionMode concurrently (syncConfigOptionModeLocked for Copilot/Goose/Cursor).
// This mirrors trySetStartupModel's locked read of b.model.
func (b *acpBase) applyStartupPermissionMode(requested string) error {
	if requested == "" {
		return nil
	}
	b.mu.Lock()
	current := b.permissionMode
	b.mu.Unlock()
	if requested == current {
		return nil
	}
	return b.setPermissionMode(requested)
}

// applyHandshakeMode sets availableModes and the permission mode from a session
// handshake (under the lock), falling back to defaultMode when the server reports none.
// A `mode` config option overrides the permission mode read from the modes channel only
// for a provider that consumes it (modeChannelPermissionMode -- Copilot/Goose/Cursor);
// for an unmapped provider it is left to applyGenericConfigOptionsLocked to
// surface read-only, matching the runtime and ClearContext paths so the option resolves
// the same way at every seam instead of being applied writably here but read-only there.
// Used by ACP providers that track a permission mode (Copilot, Goose, Cursor).
func (b *acpBase) applyHandshakeMode(handshake *acpSessionResult, defaultMode string) {
	modes := buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	mode := handshake.CurrentModeID
	if mode == "" {
		mode = defaultMode
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.availableModes = modes
	b.permissionMode = mode
	if b.modeChannel == modeChannelPermissionMode {
		b.syncConfigOptionModeLocked(handshake.ConfigOptions)
	}
}

// applyPermissionModeStartup runs the post-handshake startup sequence shared by the
// permission-mode providers (Copilot, Goose, Cursor): write the model and mode
// channels from the handshake, push the requested permission mode (fatal on
// rejection -- the agent is torn down and a startup error returned), then push the
// requested model. The model is applied last and best-effort so a rejected model id
// cannot undo the permission mode or abort an otherwise-healthy session. Cursor
// passes its normalized model id and setCursorModel for the wire-format mapping.
func (b *acpBase) applyPermissionModeStartup(handshake *acpSessionResult, opts Options, defaultMode, requestedModel string, setModel func(string) error) error {
	b.applyHandshakeModels(handshake)
	b.applyHandshakeMode(handshake, defaultMode)
	if err := b.applyStartupPermissionMode(opts.PermissionMode); err != nil {
		b.stopAndWait()
		return b.formatStartupError(acpMethodSessionSetMode, err)
	}
	b.trySetStartupModel(requestedModel, setModel)
	return nil
}

// applyPrimaryAgentStartup runs the post-handshake startup sequence shared by the
// primary-agent providers (OpenCode, Kilo): write the model channel from the
// handshake, configure the available primary agents and apply the requested one from
// extra settings (fatal on rejection -- the agent is torn down and a startup error
// returned), then push the requested model last and best-effort so a rejected model id
// cannot undo the primary-agent selection or abort an otherwise-healthy session. The
// primary-agent mirror of applyPermissionModeStartup; the two providers differ only in
// the fallback list and default-agent constant.
func (b *acpBase) applyPrimaryAgentStartup(handshake *acpSessionResult, opts Options, fallback []*leapmuxv1.AvailableOption, defaultAgent string, setModel func(string) error) error {
	b.applyHandshakeModels(handshake)
	var requestedPrimaryAgent string
	if opts.ExtraSettings != nil {
		requestedPrimaryAgent = opts.ExtraSettings[OptionGroupKeyPrimaryAgent]
	}
	if err := b.configurePrimaryAgents(handshake.Modes, handshake.CurrentModeID, requestedPrimaryAgent, fallback, defaultAgent); err != nil {
		b.stopAndWait()
		return b.formatStartupError(acpMethodSessionSetMode, err)
	}
	b.trySetStartupModel(opts.Model, setModel)
	return nil
}

// handleACPConfigOptionUpdate processes a config_option_update notification. The
// model channel is handled generically for every ACP provider; the configOptions
// `mode` select is applied as the permission mode for modeChannelPermissionMode
// providers (Copilot/Goose/Cursor) or the primary agent for modeChannelPrimaryAgent
// providers (OpenCode/Kilo); any unmapped option is surfaced read-only as a generic
// group. All
// channels mutate under a single lock so a concurrent settings read can never observe
// a half-applied update. Broadcasts happen after the lock is released.
//
// A setting change (model, primary agent, or a generic value) persists and broadcasts
// the full settings exactly once via broadcastSettingsRefresh; when only the
// permission mode changed, UpdatePermissionMode persists+broadcasts it together with
// the chat notification. A mode change that rides alongside a model/primary-agent/
// generic change emits only the chat notification, since broadcastSettingsRefresh
// already carried the new mode in its StatusChange -- avoiding a second, transiently
// stale-model broadcast.
func (b *acpBase) handleACPConfigOptionUpdate(update json.RawMessage) {
	options := parseACPConfigOptions(update)
	if len(options) == 0 {
		return
	}

	b.mu.Lock()
	oldMode := b.permissionMode
	// A launch-fixed-model provider (e.g. Reasonix) cannot switch model over ACP;
	// ignore any model select so the stored model stays in sync with the running
	// process (a model change relaunches instead, via UpdateSettings).
	var modelChanged, listChanged bool
	if !b.modelFixedAtLaunch {
		modelChanged, listChanged = b.applyConfigOptionModelsLocked(options)
	}
	var mode string
	var modeChanged, primaryAgentChanged bool
	if b.modeChannel == modeChannelPermissionMode {
		var modeListChanged bool
		mode, modeChanged, modeListChanged = b.syncConfigOptionModeLocked(options)
		listChanged = listChanged || modeListChanged
	}
	if b.modeChannel == modeChannelPrimaryAgent {
		var agentListChanged bool
		_, primaryAgentChanged, agentListChanged = b.syncConfigOptionPrimaryAgentLocked(options)
		listChanged = listChanged || agentListChanged
	}
	// Surface/sync any unmapped config option (read-only generic groups). A
	// generic value change persists via broadcastSettingsRefresh (below); a
	// generic list-only change rides the status-refresh branch.
	genericValueChanged, genericListChanged := b.applyGenericConfigOptionsLocked(options)
	sessionID := b.sessionID
	b.mu.Unlock()

	switch {
	case modelChanged || primaryAgentChanged || genericValueChanged:
		// A model, primary-agent, or generic-value change persists+broadcasts the full
		// settings in one StatusChange (which re-fetches the live model list and carries
		// the live mode and extras), so the frontend reflects the runtime switch
		// immediately. A generic value must persist via broadcastSettingsRefresh -- not
		// BroadcastStatusActive -- so the new selection survives in extra_settings.
		b.broadcastSettingsRefresh()
		if modeChanged {
			// The StatusChange above already carried the new mode; emit only the chat
			// settings_changed notification rather than a second StatusChange.
			b.sink.NotifyPermissionModeChanged(oldMode, mode)
		}
	case modeChanged:
		// Mode-only change: persist the mode, broadcast the StatusChange (which carries
		// the live model list), and emit the chat notification -- all in one call.
		b.sink.UpdatePermissionMode(mode)
	case listChanged || genericListChanged:
		// An available list changed (models, modes, primary agents, or a generic option
		// set) but no current selection did. broadcastSettingsRefresh would no-op
		// (PersistSettingsRefresh skips when model/mode/extras are unchanged), so
		// broadcast a status refresh directly -- its StatusChange re-fetches the live
		// model list and option groups, surfacing the new options.
		b.sink.BroadcastStatusActive(sessionID)
	}
}

// applyConfigOptionModelsLocked refreshes availableModels and the current model
// from the `model` select of a configOptions payload, applying modelIDNormalizer
// and modelsDecorator. It returns whether the current model changed and whether
// the available-model list changed. The caller must hold b.mu. This is the generic
// runtime model channel: it works for any ACP provider without per-provider wiring,
// so even an agent we have not special-cased keeps its model list and selection
// current across a session.
//
// The configOptions `model` select carries only that channel's models, so the
// remembered models-field catalog (modelsFieldInfos) is re-unioned -- otherwise a
// provider that splits its catalog across both channels would lose its
// models-field-only entries on every runtime update.
func (b *acpBase) applyConfigOptionModelsLocked(options []acpConfigOption) (modelChanged, listChanged bool) {
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
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
	if len(models) > 0 && !protoSliceEqual(b.availableModels, models) {
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
// config_option_update, and for the mode and generic channels alike. The option's
// CurrentValue marks the default. Shared by buildConfigOptionSelect (mode) and
// applyGenericConfigOptionsLocked (generic).
func buildOptionValues(option acpConfigOption, hiddenFilter func(string) bool) []*leapmuxv1.AvailableOption {
	built := make([]*leapmuxv1.AvailableOption, 0, len(option.Options))
	seen := make(map[string]bool, len(option.Options))
	for _, candidate := range option.Options {
		if candidate.Value == "" || seen[candidate.Value] || (hiddenFilter != nil && hiddenFilter(candidate.Value)) {
			continue
		}
		seen[candidate.Value] = true
		built = append(built, &leapmuxv1.AvailableOption{
			Id:          candidate.Value,
			Name:        titleCaseID(candidate.Value, normalizeOptionName(candidate.Name, candidate.Value)),
			Description: candidate.Description,
			IsDefault:   candidate.Value == option.CurrentValue,
		})
	}
	return built
}

// buildConfigOptionSelect converts the `mode` select of a configOptions payload
// into proto options and reports its current value, applying an optional hidden
// filter. ok is false when the payload carries no `mode` option. Shared by the
// permission-mode and primary-agent sync paths, which differ only in which field
// they store the result into.
func buildConfigOptionSelect(options []acpConfigOption, hiddenFilter func(string) bool) (built []*leapmuxv1.AvailableOption, current string, ok bool) {
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, acpConfigOptionIDMode)
	if !ok {
		return nil, "", false
	}
	return buildOptionValues(option, hiddenFilter), option.CurrentValue, true
}

// syncConfigOptionSelectLocked refreshes one secondary channel (the permission
// mode or the primary agent) from the `mode` select of a configOptions payload. It
// writes the available-option list and the current value into the caller-supplied
// fields, and reports the new value, whether the current value changed, and whether
// the available list changed. The caller must hold b.mu. Shared by
// syncConfigOptionModeLocked and syncConfigOptionPrimaryAgentLocked, which differ
// only in the hidden filter and the two target fields.
func (b *acpBase) syncConfigOptionSelectLocked(
	options []acpConfigOption,
	hiddenFilter func(string) bool,
	available *[]*leapmuxv1.AvailableOption,
	currentField *string,
) (value string, changed, listChanged bool) {
	built, current, ok := buildConfigOptionSelect(options, hiddenFilter)
	if !ok {
		return "", false, false
	}
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
	// hit primaryAgentExtras' keep-stored nil and desync memory from the DB. This is the
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
// b.mu. Used by ACP providers whose configOptions `mode` maps to the permission
// mode (Copilot, Goose, Cursor).
func (b *acpBase) syncConfigOptionModeLocked(options []acpConfigOption) (string, bool, bool) {
	return b.syncConfigOptionSelectLocked(options, nil, &b.availableModes, &b.permissionMode)
}

// syncConfigOptionPrimaryAgentLocked refreshes currentPrimaryAgent and
// availablePrimaryAgents from the `mode` select of a configOptions payload,
// returning the new value, whether it changed, and whether the available list
// changed. The caller must hold b.mu. Used by ACP providers whose configOptions
// `mode` maps to the primary agent (OpenCode, Kilo), so a server-initiated runtime
// primary-agent switch is reflected -- the mirror of syncConfigOptionModeLocked for
// the permission-mode providers.
func (b *acpBase) syncConfigOptionPrimaryAgentLocked(options []acpConfigOption) (string, bool, bool) {
	return b.syncConfigOptionSelectLocked(options, b.primaryAgentHiddenFilter, &b.availablePrimaryAgents, &b.currentPrimaryAgent)
}

// applyGenericConfigOptionsLocked surfaces the config-option selectors the model and
// mode channels did not claim as additional, read-only option groups, recording
// each one's current value. It mirrors applyConfigOptionModelsLocked: the caller
// holds b.mu, and it reports whether a current value changed (valueChanged) versus
// only the option set changed (listChanged) so handleACPConfigOptionUpdate can route
// a value change through broadcastSettingsRefresh (which persists it) and a list-only
// change through a status refresh.
//
// The claimed model and mode options are excluded by identity (the matched entry's
// id), so the permission-mode/primary-agent group -- the claimed "mode" -- is never
// double-rendered as a generic group.
//
// Keep-stored guard (critical): a payload carrying no unmapped option leaves the
// stored generic state untouched and returns (false, false). A model-only runtime
// config_option_update -- the common case -- must not wipe previously surfaced
// generics, mirroring applyConfigOptionModelsLocked's early-out and the len>0
// empty-list discipline.
func (b *acpBase) applyGenericConfigOptionsLocked(options []acpConfigOption) (valueChanged, listChanged bool) {
	// Capture the claimed model/mode options by identity so we exclude exactly those
	// entries -- an unclaimed selector with a coincidental id is still surfaced.
	claimedModelID, claimedModel := "", false
	if option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, acpConfigOptionIDModel); ok {
		claimedModelID, claimedModel = option.ID, true
	}
	// The mode option is "claimed" only by a provider that actually consumes it (a
	// permission-mode or primary-agent provider). A provider whose mode channel is
	// unmapped would otherwise exclude a configOptions `mode` here without
	// applying it anywhere -- silently dropping it. Surfacing it read-only is the safe
	// fallback.
	claimedModeID, claimedMode := "", false
	if b.modeChannel != modeChannelUnmapped {
		if option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, acpConfigOptionIDMode); ok {
			claimedModeID, claimedMode = option.ID, true
		}
	}

	var groups []*leapmuxv1.AvailableOptionGroup
	values := make(map[string]string)
	for _, option := range options {
		if option.ID == "" || !isSelectableConfigOption(option) {
			continue
		}
		if (claimedModel && option.ID == claimedModelID) || (claimedMode && option.ID == claimedModeID) {
			continue
		}
		// Never surface a generic group under a reserved proto key: the mapped
		// permission-mode/primary-agent group already owns that key, and a second group
		// with the same key would double-list it in AvailableOptionGroups.
		if option.ID == OptionGroupKeyPermissionMode || option.ID == OptionGroupKeyPrimaryAgent {
			continue
		}
		groups = append(groups, &leapmuxv1.AvailableOptionGroup{
			Key:     option.ID,
			Label:   nameOrID(option.Name, option.ID),
			Options: buildOptionValues(option, nil),
		})
		values[option.ID] = option.CurrentValue
	}

	// Keep-stored guard: only rebuild when the payload actually carried an unmapped
	// option, so a model-only update can't wipe the stored generics.
	if len(groups) == 0 {
		return false, false
	}

	// maps.Equal tells an idempotent re-send from a real value change; protoSliceEqual
	// (proto.Equal per group) compares key, label, and nested options in one call, so a
	// future field added to AvailableOptionGroup is included automatically rather than
	// silently ignored by a hand-rolled key/label-only comparison.
	valueChanged = !maps.Equal(b.genericOptionValues, values)
	listChanged = !protoSliceEqual(b.genericOptionGroups, groups)
	b.genericOptionGroups = groups
	b.genericOptionValues = values
	return valueChanged, listChanged
}

// nameOrID returns the trimmed display name, falling back to the id when the name is
// blank. Used to label a generic config-option group from the option's own name.
func nameOrID(name, id string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return id
}

// mergeGenericExtrasLocked overlays the current values of the generic config options
// onto a base extras map. The base map (e.g. primaryAgentExtras) owns its keys: the
// generics are written first and the base overlaid on top, so a generic option that
// coincidentally shares a base key (primaryAgent) can never clobber it. The caller
// must hold b.mu.
//
// Returns nil only when nothing is being reported -- no base AND no surfaced generic
// options -- which preserves the PersistSettingsRefresh keep-stored contract and the
// primaryAgentExtras nil-vs-empty discipline. When generic options ARE surfaced but
// all their current values are empty (an agent cleared an optional axis), it returns
// a non-nil map so PersistSettingsRefresh replaces the stored extras wholesale and the
// cleared value doesn't linger -- returning nil there would keep the stale value.
func (b *acpBase) mergeGenericExtrasLocked(base map[string]string) map[string]string {
	if len(base) == 0 && len(b.genericOptionValues) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(b.genericOptionValues))
	for id, value := range b.genericOptionValues {
		if value != "" {
			merged[id] = value
		}
	}
	for k, v := range base {
		merged[k] = v
	}
	return merged
}

// broadcastSettingsRefresh persists and broadcasts the agent's current settings.
// It reads the live model/mode/primary-agent state, so it serves both permission-
// mode providers (currentPrimaryAgent == "" -> nil extras) and primary-agent
// providers (permissionMode == "" -> the stored mode is preserved by the sink).
func (b *acpBase) broadcastSettingsRefresh() {
	b.mu.Lock()
	model := b.model
	mode := b.permissionMode
	// Carry the live generic values too: PersistSettingsRefresh replaces the stored
	// extras wholesale (non-nil), so a model/primary-agent change that did not also
	// touch the generics must still re-include them or it would wipe them.
	extras := b.mergeGenericExtrasLocked(primaryAgentExtras(b.currentPrimaryAgent))
	b.mu.Unlock()
	b.sink.PersistSettingsRefresh(model, "", mode, extras)
}

// buildACPModes converts a list of acpModeInfo into proto AvailableOption messages.
// If filter is non-nil, modes for which filter returns true are skipped.
func buildACPModes(modes []acpModeInfo, currentModeID string, filter func(id string) bool) []*leapmuxv1.AvailableOption {
	result := make([]*leapmuxv1.AvailableOption, 0, len(modes))
	for _, mode := range modes {
		if mode.ID == "" {
			continue
		}
		if filter != nil && filter(mode.ID) {
			continue
		}
		name := titleCaseID(mode.ID, mode.Name)
		result = append(result, &leapmuxv1.AvailableOption{
			Id:          mode.ID,
			Name:        name,
			Description: mode.Description,
			IsDefault:   mode.ID == currentModeID,
		})
	}
	return result
}

func parseACPConfigOptions(raw json.RawMessage) []acpConfigOption {
	var payload struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("acp config options unmarshal failed", "error", err)
		return nil
	}
	return payload.ConfigOptions
}

// AvailableModels returns the models reported by the ACP provider.
func (b *acpBase) AvailableModels() []*leapmuxv1.AvailableModel {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.availableModels
}

// setModelRPC sends a session/set_model request for the given wire model id
// WITHOUT touching b.model. Callers store the local model themselves -- setModel
// stores the same id, while Cursor's setCursorModel stores the normalized
// (display) id rather than the wire id. Keeping the field write out of the RPC
// avoids a window where b.model briefly holds the wire id (e.g. "default[]")
// that a concurrent CurrentSettings() read could observe and persist.
func (b *acpBase) setModelRPC(wireModel string) error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modelId":   wireModel,
	})
	if err != nil {
		return fmt.Errorf("marshal setModel params: %w", err)
	}
	resp, err := b.sendRequest(acpMethodSessionSetModel, json.RawMessage(params), b.APITimeout())
	if err != nil {
		return err
	}
	return jsonRPCResultError(resp)
}

// setModel sends a session/set_model request and updates the local model field.
func (b *acpBase) setModel(model string) error {
	if err := b.setModelRPC(model); err != nil {
		return err
	}
	b.mu.Lock()
	b.model = model
	b.mu.Unlock()
	return nil
}

// acpSetMode sends a session/set_mode request and returns nil on success.
// If available is non-empty and modeID is not found, an error is returned.
func (b *acpBase) acpSetMode(modeID string, available []*leapmuxv1.AvailableOption) error {
	if len(available) > 0 && !hasACPOption(available, modeID) {
		return fmt.Errorf("unknown mode: %s", modeID)
	}

	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    modeID,
	})
	if err != nil {
		return fmt.Errorf("marshal setMode params: %w", err)
	}
	resp, err := b.sendRequest(acpMethodSessionSetMode, json.RawMessage(params), b.APITimeout())
	if err != nil {
		return err
	}
	return jsonRPCResultError(resp)
}

// cancelSession sends a session/cancel notification.
func (b *acpBase) cancelSession() error {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	params, err := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
	if err != nil {
		return fmt.Errorf("marshal cancel params: %w", err)
	}
	return b.sendNotification(acpMethodSessionCancel, json.RawMessage(params))
}

// Interrupt aborts the active ACP turn by sending the
// `session/cancel` notification — the wire format every ACP server
// in our roster (Cursor, Copilot, Kilo, OpenCode, Goose)
// recognizes, and the one acpProvider.IsInterrupt classifier expects.
//
// Embedded into every ACP-derived agent (CursorAgent,
// CopilotCLIAgent, KiloAgent, OpenCodeAgent, GooseAgent) via the
// acpBase embedding chain, so a single implementation covers all five
// providers.
//
// No-op when no session has been opened (sessionID still empty) so
// the worker InterruptAgent RPC can be called unconditionally without
// the caller having to wait for the ACP handshake to complete.
func (b *acpBase) Interrupt() error {
	if b.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	if sessionID == "" {
		return nil
	}
	return b.cancelSession()
}

// capitalizeFirst returns s with its first rune upper-cased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		return string(unicode.ToUpper(r)) + s[len(string(r)):]
	}
	return s
}

// normalizeOptionName trims an ACP option's server-reported display name and
// treats a blank or id-equal name as absent (""), so titleCaseID falls back to
// title-casing the id. Applied on both the handshake (buildPrimaryAgentOptions)
// and runtime (buildConfigOptionSelect) option-building paths so an option renders
// identically regardless of which path produced it -- OpenCode-family agents often
// report name == id or whitespace-only names.
func normalizeOptionName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == id {
		return ""
	}
	return name
}

// titleCaseID returns name if it is a distinct display name (non-empty and
// different from id). Otherwise it title-cases the id by splitting on
// underscores or hyphens, capitalizing each word, and joining with spaces
// (e.g. "smart_approve" → "Smart Approve", "full-auto" → "Full Auto").
func titleCaseID(id, name string) string {
	if name != "" && name != id {
		return name
	}
	if id == "" {
		return ""
	}
	// Determine separator: prefer underscore, fall back to hyphen.
	sep := "_"
	if !strings.Contains(id, "_") && strings.Contains(id, "-") {
		sep = "-"
	}
	parts := strings.Split(id, sep)
	for i, p := range parts {
		parts[i] = capitalizeFirst(p)
	}
	return strings.Join(parts, " ")
}

// hasACPOption returns true if any option in the slice has the given id.
func hasACPOption(options []*leapmuxv1.AvailableOption, id string) bool {
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

func (b *acpBase) handlePlan(update json.RawMessage) {
	if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, update, SpanInfo{}); err != nil {
		slog.Error("persist acp plan", "agent_id", b.agentID, "error", err)
	}
}

func (b *acpBase) handleRequestPermission(id string, content []byte) {
	if id == "" {
		slog.Warn("acp requestPermission missing id", "agent_id", b.agentID)
		return
	}
	b.sink.PersistControlRequest(id, content)
	b.sink.BroadcastControlRequest(id, content)
}

// handleOutput dispatches a single parsed output line using the provider's
// extraSessionUpdate handler. Used as the outputHandler for readOutputLoop.
func (b *acpBase) handleOutput(line *parsedLine) {
	slog.Debug("acp HandleOutput", "provider", b.providerName, "agent_id", b.agentID, "method", line.Method, "len", len(line.Raw))
	b.handleACPOutput(line, b.extraSessionUpdate, b.extraMethod)
}

// HandleOutput processes a single JSONL notification from an ACP provider.
func (b *acpBase) HandleOutput(content []byte) {
	b.handleOutput(parseLine(content))
}

// handleACPOutput is the shared output dispatcher for all ACP providers.
// It routes session updates and permission requests, persisting anything else.
func (b *acpBase) handleACPOutput(line *parsedLine, extraSessionUpdate acpSessionUpdateHandler, extraMethod acpMethodHandler) {
	switch line.Method {
	case acpMethodSessionUpdate:
		b.handleACPSessionUpdate(line.Params, extraSessionUpdate)
	case acpMethodSessionRequestPermission:
		b.handleRequestPermission(line.IDString(), line.Raw)
	default:
		if extraMethod != nil && extraMethod(line) {
			return
		}
		if err := b.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, line.Raw, SpanInfo{}); err != nil {
			slog.Error("acp persist notification", "agent_id", b.agentID, "method", line.Method, "error", err)
		}
	}
}

// doSendACPPrompt sends a single ACP prompt RPC and processes the response.
// Used as the promptFunc for all ACP agents; handleResponse varies per provider.
//
// No timeout on the RPC: the turn unblocks via response, process exit, or
// ctx cancel (the user interrupting). A wall-clock cap would just kill
// long-but-legitimate turns.
func (b *acpBase) doSendACPPrompt(content string, attachments []*leapmuxv1.Attachment, handleResponse func(json.RawMessage)) {
	b.sendPrompt(content, attachments,
		func(params json.RawMessage) (json.RawMessage, error) {
			return b.sendRequest(acpMethodSessionPrompt, params, 0)
		},
		handleResponse,
	)
}
