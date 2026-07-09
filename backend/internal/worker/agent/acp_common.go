package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
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
	// acpMethodSessionSetConfigOption is the config-option setter (ACP's
	// session/set_config_option): params {sessionId, configId, value}, returning the
	// refreshed configOptions list. Used to write the mutable option groups
	// (e.g. OpenCode/Kilo "effort", Copilot "reasoning_effort"/"allow_all") that have
	// no dedicated set_model/set_mode channel.
	acpMethodSessionSetConfigOption = "session/set_config_option"
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
// shared by all ACP agents and CodexAgent.
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
// family, configOptions `mode` surfaced as a option group.
type acpModeChannel int

const (
	// modeChannelUnmapped: the provider tracks a permission mode but does NOT consume
	// the configOptions `mode` select for it -- it drives the mode through the native
	// modes/current_mode_update channel instead. A configOptions `mode` is
	// surfaced as a mutable option group rather than applied as the permission mode.
	// This is the zero-value default; no provider currently selects it.
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
	// modelSetter, when set, overrides how a model is written over ACP -- Cursor maps
	// the model id to its wire form (setCursorModel) before session/set_model. nil falls
	// back to the base setModel. effectiveSetModel resolves it; UpdateSettings / reapply
	// use that, so one body serves Cursor and the plain providers alike.
	modelSetter func(string) error
	// modelDecorator, when set, post-processes each built model in place -- e.g.
	// Cursor parses the metadata baked into its bracketed model ids (effort /
	// thinking / context) into the ModelInfo's Description and ContextWindow, which
	// the bare server-reported name omits.
	modelDecorator func(*ModelInfo)
	// modeChannel selects how the configOptions `mode` select maps to this provider's
	// secondary setting, and thereby which family the provider belongs to. It replaces
	// the old syncsPermissionMode/syncsPrimaryAgent bool pair so the illegal "both set"
	// state is unrepresentable and every family-conditional site reads one field.
	modeChannel acpModeChannel
	// secondaryChannelOnce/secondaryChannelCache memoize the resolved secondary channel.
	// modeChannel is fixed at construction (configure) and the channel's field/list POINTERS
	// and closures all capture b (stable), so the resolution is invariant for the agent's
	// lifetime -- secondaryChannel() builds it once rather than rebuilding the struct-of-closures
	// on each of its ~7 per-operation callers. Resolved lazily on first use (after configure has
	// set modeChannel), never copied (acpBase is always used by pointer).
	secondaryChannelOnce  sync.Once
	secondaryChannelCache acpSecondaryChannel
	// modelFixedAtLaunch marks a provider whose model is selected once at process
	// launch (e.g. Reasonix's `--model` flag) and cannot change over ACP. For such
	// providers a server config_option_update must not overwrite the model, or the
	// stored/broadcast model would drift from what the process is actually running.
	modelFixedAtLaunch bool
	// effortConfigID is the daemon config-option id this provider drives its reasoning-effort
	// axis through when that id is a provider CONVENTION rather than the well-known "effort"
	// (Copilot "reasoning_effort", Goose "thinking_effort"). It is "" for a provider whose
	// effort axis IS "effort" (OpenCode/Kilo -- the override maps to "effort" directly) or that
	// has no effort axis (Cursor/Reasonix). applyStartupOptions maps the env-effort override
	// (stored under the well-known "effort" id) onto it, so the operator default is re-pushed
	// regardless of the daemon's id. Declaring it per provider -- rather than scanning the live
	// option set for ANY well-known effort id -- means a coincidental second axis a daemon
	// advertises can't be mistaken for this provider's effort axis and have the override
	// double-pushed onto it. A daemon that instead tags its axis with the ACP `thought_level`
	// category (a self-describing spec signal) is still auto-discovered; see startupEffortConfigID.
	// Immutable after configure, so it is read without b.mu.
	effortConfigID string
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
	availableModels          []*ModelInfo
	// modelsFieldInfos holds the models reported through the SessionModelState
	// `models` field at the last full session response (handshake or ClearContext).
	// A runtime config_option_update carries only the configOptions `model` select,
	// so applyConfigOptionModelsLocked re-unions these to keep models-field-only
	// entries from vanishing mid-session for providers that split their catalog.
	modelsFieldInfos       []acpModelInfo
	availableModes         []*leapmuxv1.AvailableOption
	availablePrimaryAgents []*leapmuxv1.AvailableOption
	// secondaryFallback is the static option list for this provider's secondary axis
	// (permission modes or primary agents), served by OptionGroups before the session
	// reports its catalog. Set in configure; nil for a model-only provider (Reasonix).
	// Sourcing it here lets the one shared OptionGroups serve every ACP family without a
	// per-provider override -- the same fallback staticSecondaryGroup uses at registration.
	secondaryFallback []*leapmuxv1.AvailableOption
	// options bundles the server-driven config-option bookkeeping for the selectors the model
	// and mode channels do not claim. All of it is guarded by b.mu -- the same lock every
	// other acpBase field uses -- so a refresh can pair an option change with the
	// model/secondary under one critical section (see optionState). Carrying NO mutex of its
	// own is deliberate.
	options optionState
	// optionWriteMu serializes a whole multi-option write batch (applyOptionUpdates
	// / reapplyOptions / applyStartupOptions) against another batch, so two
	// concurrent batches can't interleave their session/set_config_option RPCs and validate
	// each id against a half-applied map. It is an OPERATION lock, distinct from the b.mu
	// STATE lock, and is always acquired BEFORE b.mu (never the reverse) to avoid a cycle.
	// It deliberately does NOT guard handleACPConfigOptionUpdate: that runs on the reader
	// goroutine that also delivers these RPCs' responses, so blocking it on a batch in
	// flight would deadlock; a server-initiated config_option_update may still fold mid-batch.
	optionWriteMu sync.Mutex
	// sessionMu serializes the session lifecycle (session/new + the sessionID swap, held
	// under the write lock by newSessionLocked) against every session/* RPC
	// (setModelViaConfigOption, acpSetMode, setConfigOption, cancelSession, each holding the read lock for its
	// capture-sessionID-and-send via withSessionID). Without it a write could capture the
	// pre-clear sessionID and send AFTER a concurrent ClearContext replaced the session,
	// targeting a torn-down session. It is acquired BEFORE b.mu, and BELOW optionWriteMu in
	// the lock order (optionWriteMu -> sessionMu -> b.mu); ClearContext releases it before
	// reapplySettings, whose RPCs re-acquire the read lock per call.
	sessionMu         sync.RWMutex
	turnAssistantText strings.Builder
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

// handleACPPromptResponse extracts accumulated turn text, persists the prompt
// response, and resets the tool-use count.
func (b *acpBase) handleACPPromptResponse(resp json.RawMessage) {
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
		// Shared model channel for every ACP provider; mode handled per-provider.
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
	sessionID, resp, ok := b.newSessionLocked()
	if !ok {
		return "", false
	}
	// The session was replaced; drop any in-flight thinking-token estimate too.
	b.thinkingTokens.reset()

	b.sink.UpdateSessionID(sessionID)

	// reapplySettings re-applies model/mode/options against the NEW session; it runs
	// AFTER sessionMu is released (newSessionLocked returned) because its RPCs re-acquire
	// sessionMu.RLock per call -- holding the write lock across them would deadlock.
	if b.reapplySettings != nil {
		b.reapplySettings()
	}
	if b.refreshFromSession != nil {
		b.refreshFromSession(resp)
	}
	return sessionID, true
}

// newSessionLocked sends session/new and atomically swaps b.sessionID, holding
// sessionMu.Lock for the whole exchange so no concurrent session/* RPC (each holding
// sessionMu.RLock around its own capture-and-send via withSessionID) can straddle the
// replacement -- such a write would otherwise target the just-replaced session. Returns
// the new session id and the raw response (for refreshFromSession), or ok=false on failure.
func (b *acpBase) newSessionLocked() (sessionID string, resp json.RawMessage, ok bool) {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	_, params := buildACPSessionRequest("", b.workingDir, acpMethodSessionNew, "")
	resp, err := b.sendRequest(acpMethodSessionNew, json.RawMessage(params), b.APITimeout())
	if err != nil {
		slog.Error("acp ClearContext failed", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", nil, false
	}
	if err := jsonRPCResultError(resp); err != nil {
		slog.Error("acp ClearContext: RPC error", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return "", nil, false
	}

	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &session); err != nil || session.SessionID == "" {
		slog.Error("acp ClearContext: invalid response", "provider", b.providerName, "agent_id", b.agentID, "error", err, "response", string(resp))
		return "", nil, false
	}

	b.mu.Lock()
	b.sessionID = session.SessionID
	b.turnAssistantText.Reset()
	b.turnThoughtText.Reset()
	b.mu.Unlock()
	return session.SessionID, resp, true
}

// withSessionID runs fn with the agent's current session id, holding sessionMu.RLock for
// the whole call so a concurrent ClearContext -- which holds sessionMu.Lock around
// session/new and the sessionID swap (newSessionLocked) -- cannot replace the session
// mid-RPC. fn therefore runs entirely against the pre-swap session or starts after the
// swap, never straddling it (which would target the just-replaced session). Shared by
// every session/* RPC so they coordinate with ClearContext the same way.
func (b *acpBase) withSessionID(fn func(sessionID string) error) error {
	b.sessionMu.RLock()
	defer b.sessionMu.RUnlock()
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	return fn(sessionID)
}

// secondaryAxis is the fixed (option id, label, order) presentation of an ACP provider's
// secondary axis -- permission mode (Copilot/Goose/Cursor) or primary agent (OpenCode/Kilo).
// The triple is fixed per axis (every permission-mode provider labels it "Mode", every
// primary-agent provider "Primary Agent"), so each is declared exactly once below and shared
// by the live channel (secondaryChannel) and the static per-provider registration
// (staticSecondaryGroup) -- the two can't disagree about a channel's id, label, or sort order.
type secondaryAxis struct {
	optionID string
	label    string
	order    int32
}

var (
	permissionModeAxis = secondaryAxis{optionID: OptionIDPermissionMode, label: "Mode", order: OptionOrderPermissionMode}
	primaryAgentAxis   = secondaryAxis{optionID: OptionIDPrimaryAgent, label: "Primary Agent", order: OptionOrderPrimaryAgent}
)

// secondaryAxisFor maps a mode channel to its fixed presentation axis.
func secondaryAxisFor(modeChannel acpModeChannel) secondaryAxis {
	if modeChannel == modeChannelPrimaryAgent {
		return primaryAgentAxis
	}
	return permissionModeAxis
}

// secondaryGroup builds the secondary-axis (permission-mode / primary-agent) group from its
// axis triple, option list, and current value. A non-empty `current` stamps the live selection;
// "" yields the static-fallback shape (proto omits CurrentValue at its zero value). DefaultValue
// is the provider default (default-or-first), NOT the live current -- it marks which option the
// picker badges as the default, and that badge must not follow the user's selection around. The
// live (secondaryOptionGroupLocked) and static-fallback (staticSecondaryGroup) groups share this
// one builder so they can't drift in which fields they stamp.
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

// staticSecondaryGroup builds the one-element static-fallback registration for a provider
// whose only mapped axis is the secondary channel (a permission-mode or primary-agent
// group). It sources the (id, label, order) triple from secondaryAxisFor so it matches the
// live group secondaryOptionGroupLocked builds, sources the option list from the same
// fallback function the live OptionGroups path uses (via the provider's secondaryFallback)
// so the two can't drift, and carries the matching order so the static fallback (served
// before the session reports its catalog) doesn't sort the group ahead of the model/effort
// groups. Each provider passes only its mode channel and fallback list. The default badge
// (default-or-first) lets a fresh tab's Mode / Primary Agent group show a marked default
// before the handshake lands.
func staticSecondaryGroup(modeChannel acpModeChannel, options []*leapmuxv1.AvailableOption) []*leapmuxv1.AvailableOptionGroup {
	return []*leapmuxv1.AvailableOptionGroup{secondaryGroup(secondaryAxisFor(modeChannel), options, "")}
}

// registeredSecondaryFallback returns the secondary-axis fallback option list a provider
// declared at registration -- the .Options of the static group staticSecondaryGroup stored in
// the factory registry. acpStart seeds a running agent's b.secondaryFallback from this so each
// provider names its fallback list exactly ONCE (in its registerXxx call) rather than also in
// configure. secondaryGroup stamps Options verbatim, so the unwrapped list is the same slice the
// registration passed in. Returns nil for a provider with no mapped secondary axis (the unmapped
// channel, e.g. Reasonix) or one whose registry entry carries no such group.
func registeredSecondaryFallback(provider leapmuxv1.AgentProvider, modeChannel acpModeChannel) []*leapmuxv1.AvailableOption {
	if modeChannel == modeChannelUnmapped {
		return nil
	}
	g := optionids.GroupByID(agentFactoryRegistry[provider].optionGroups, secondaryAxisFor(modeChannel).optionID)
	return g.GetOptions()
}

// acpSecondaryChannel bundles the secondaryAxis (option id, label, order) with the field
// pointer, setter, and log key for an ACP provider's secondary axis. secondaryChannel derives it
// from modeChannel in ONE place, so the UpdateSettings / reapply / refresh paths can't disagree
// about which field they touch.
type acpSecondaryChannel struct {
	secondaryAxis
	// modeChannel is the family this channel routes as, so consumers can ask the channel which
	// family it is (routesAsPermissionMode / routesAsPrimaryAgent) instead of re-reading
	// b.modeChannel and re-deriving the distinction that secondaryChannel() already owns.
	modeChannel acpModeChannel
	field       *string
	set         func(string) error
	logKey      string
	// available points at the in-memory available-option list for this axis (&b.availableModes
	// or &b.availablePrimaryAgents), so the read and refresh paths share one slice without
	// re-deriving it from modeChannel. It points at the FIELD, not the current slice header, so
	// it tracks a rebuild's reassignment. Deref under b.mu.
	available *[]*leapmuxv1.AvailableOption
	// rebuild replaces *available from the native modes channel, keeping the prior list when
	// the rebuild is empty. Caller holds b.mu.
	rebuild func(modes []acpModeInfo, reported string)
	// hiddenFilter returns "" for a reported value the picker must not adopt (a hidden
	// primary-agent pseudo-agent), else the value unchanged. Permission-mode has none.
	hiddenFilter func(reported string) string
	// syncConfigOverride applies a configOptions override for this axis, returning the resolved
	// value and whether the current value / available list changed -- so the runtime update path
	// consumes the resolved channel instead of re-branching on the family to pick which Locked
	// method to call. nil for a family with no override (the unmapped channel), reproducing the
	// old switch's no-default no-op.
	syncConfigOverride func(configOptions []acpConfigOption) (value string, changed, listChanged bool)
	// persistShape returns how the secondary value is persisted: a primary-agent provider
	// carries it in the option values (primaryAgentOptions), a permission-mode provider in
	// PersistSettingsRefresh's own mode arg.
	persistShape func(value string) (optionsBase map[string]string, persistMode string)
}

// secondaryChannel resolves, ONCE, every per-family fact about the agent's secondary axis
// (permission mode vs primary agent): the id/label/order, the current-value field and its
// setter, the available-list pointer, and the rebuild / hidden-filter / config-override /
// persist-shape closures the refresh path needs. Every consumer (UpdateSettings,
// reapplyModelAndSecondary, the session-refresh helpers, secondaryOptionGroupLocked) reads
// the resolved value instead of re-branching on b.modeChannel, so the family distinction
// lives in exactly one place. The closures capture b and require the owning b.mu where they
// touch b's fields (the refresh path holds it). Memoized via secondaryChannelOnce -- modeChannel
// is fixed at construction, so the resolution never changes after the first call.
func (b *acpBase) secondaryChannel() acpSecondaryChannel {
	b.secondaryChannelOnce.Do(func() { b.secondaryChannelCache = b.buildSecondaryChannel() })
	return b.secondaryChannelCache
}

func (b *acpBase) buildSecondaryChannel() acpSecondaryChannel {
	sc := acpSecondaryChannel{secondaryAxis: secondaryAxisFor(b.modeChannel), modeChannel: b.modeChannel}
	if b.modeChannel == modeChannelPrimaryAgent {
		sc.field, sc.set, sc.logKey = &b.currentPrimaryAgent, b.setSecondary, "primaryAgent"
		sc.available = &b.availablePrimaryAgents
		sc.rebuild = func(modes []acpModeInfo, reported string) {
			if rebuilt := b.buildPrimaryAgentOptions(modes, reported); len(rebuilt) > 0 {
				b.availablePrimaryAgents = rebuilt
			}
		}
		sc.hiddenFilter = func(reported string) string {
			if b.primaryAgentHiddenFilter != nil && b.primaryAgentHiddenFilter(reported) {
				return ""
			}
			return reported
		}
		sc.syncConfigOverride = b.syncConfigOptionPrimaryAgentLocked
		sc.persistShape = func(value string) (map[string]string, string) {
			return primaryAgentOptions(value), ""
		}
	} else {
		// The else branch maps permission-mode AND the unmapped channel to the permission-mode
		// field/setter (preserving secondaryAxisFor's mapping). The native modes channel carries
		// permission modes; an unmapped provider (Reasonix) never reaches the refresh path, so
		// rebuild/hiddenFilter/persistShape are wired to the permission-mode shapes but
		// unreachable for it.
		sc.field, sc.set, sc.logKey = &b.permissionMode, b.setSecondary, "permissionMode"
		sc.available = &b.availableModes
		sc.rebuild = func(modes []acpModeInfo, reported string) {
			if rebuilt := buildACPModes(modes, reported, nil); len(rebuilt) > 0 {
				b.availableModes = rebuilt
			}
		}
		sc.hiddenFilter = func(reported string) string { return reported }
		// Only a permission-mode provider has a configOptions override; the unmapped channel
		// keeps syncConfigOverride nil to reproduce the old switch's no-default no-op.
		if b.modeChannel == modeChannelPermissionMode {
			sc.syncConfigOverride = b.syncConfigOptionModeLocked
		}
		sc.persistShape = func(value string) (map[string]string, string) {
			return nil, value
		}
	}
	return sc
}

// routesAsPermissionMode reports whether this secondary channel is the permission-mode family,
// so a consumer can ask the resolved channel instead of re-reading b.modeChannel and re-deriving
// the family distinction secondaryChannel() already owns.
func (sc acpSecondaryChannel) routesAsPermissionMode() bool {
	return sc.modeChannel == modeChannelPermissionMode
}

// routesAsPrimaryAgent reports whether this secondary channel is the primary-agent family.
func (sc acpSecondaryChannel) routesAsPrimaryAgent() bool {
	return sc.modeChannel == modeChannelPrimaryAgent
}

// effectiveSetModel returns the model writer, preferring the provider's override
// (Cursor's setCursorModel, which maps the id to its wire form) over the base setModel.
func (b *acpBase) effectiveSetModel() func(string) error {
	if b.modelSetter != nil {
		return b.modelSetter
	}
	return b.setModel
}

// reapplyModelAndSecondary re-applies the current model and the secondary setting
// (permission mode or primary agent, per modeChannel) after a session/new, then the
// config options. The model setter and secondary channel are derived from the provider,
// so one body serves every ACP family -- including Cursor's wire-mapped model setter.
func (b *acpBase) reapplyModelAndSecondary() {
	sc := b.secondaryChannel()
	b.mu.Lock()
	model, sec := b.model, *sc.field
	// Snapshot the stored option selections BEFORE the model re-push. The model write folds
	// the fresh session's option defaults into b.options.values (and raiseEffortOffNone may
	// raise a "none" effort to "high"), so reading b.options.values AFTER the write would
	// re-push those server defaults, not the user's choice -- silently losing a persisted
	// non-"high" effort. Re-pushing from this pre-write snapshot is what makes the stored
	// selection survive a context clear.
	storedOptions := maps.Clone(b.options.values)
	b.mu.Unlock()
	acpApplySetting(b.providerName, b.agentID, "model", model, b.effectiveSetModel())
	acpApplySetting(b.providerName, b.agentID, sc.logKey, sec, sc.set)
	b.reapplyOptions(storedOptions)
}

// setSecondary sends a session/set_mode RPC for the agent's secondary axis (permission mode for
// Copilot/Goose/Cursor, primary agent for OpenCode/Kilo) and writes the resolved value into the
// corresponding local field. It reads the available-list and field POINTERS off secondaryChannel()
// rather than naming b.availableModes/b.permissionMode (or their primary-agent twins) directly, so
// the former setPermissionMode/setPrimaryAgent twins collapse into one body and "which field this
// axis touches" lives only in secondaryChannel(). modeChannel is fixed at construction, so the
// re-derivation here resolves the same channel the caller's sc did.
func (b *acpBase) setSecondary(value string) error {
	sc := b.secondaryChannel()
	b.mu.Lock()
	available := *sc.available
	b.mu.Unlock()

	if err := b.acpSetMode(value, available); err != nil {
		return err
	}
	b.mu.Lock()
	*sc.field = value
	b.mu.Unlock()
	return nil
}

// UpdateSettings applies a model + secondary (permission mode / primary agent) change
// and any mutable config options (effort / reasoning_effort / allow_all), in one
// body for every ACP family. The secondary channel, model setter, and model normalizer
// are derived from the provider (secondaryChannel / effectiveSetModel / modelIDNormalizer),
// so Cursor -- whose model writes map to a wire id -- no longer needs its own override.
func (b *acpBase) UpdateSettings(options optionmap.Map) bool {
	sc := b.secondaryChannel()
	model := options[OptionIDModel]
	if b.modelIDNormalizer != nil {
		model = b.modelIDNormalizer(model)
	}
	secondary := options[sc.optionID]

	// The service hands UpdateSettings the FULL merged options map on every change, so
	// only push the model / secondary axes when the requested value actually differs
	// from the current selection -- otherwise a change to one axis (e.g. effort) would
	// re-issue a redundant session/set_model and session/set_mode for the unchanged
	// model/mode. This mirrors the value != current guard applyOptionUpdates
	// already applies to the option axes. A skipped (unchanged) axis counts as success.
	b.mu.Lock()
	curModel, curSecondary := b.model, *sc.field
	// Capture the structure generation so we can tell, after the writes below, whether this live
	// change altered the option-group SET (see the BroadcastStatusActive call). Comparing the
	// generation rather than snapshotting b.options.groups and diffing it against the post-write
	// slice is deliberate: the reader goroutine folds a server-initiated config_option_update under
	// b.mu with no shared lock against this path, so it can reassign b.options.groups in the window
	// between our two reads -- a slice diff would then read the reader's structure as our "after",
	// spuriously broadcasting its change as ours, or (when it reverts a structure WE changed)
	// suppressing our broadcast entirely. The monotonic counter sidesteps both: a difference means a
	// structural fold happened during our span, and a reader-only fold merely yields a harmless
	// idempotent broadcast carrying the live catalog (as before).
	structureGenBefore := b.options.structureGen
	b.mu.Unlock()

	ok := true
	if model != "" && model != curModel {
		ok = acpApplySetting(b.providerName, b.agentID, "model", model, b.effectiveSetModel()) && ok
	}
	if secondary != "" && secondary != curSecondary {
		ok = acpApplySetting(b.providerName, b.agentID, sc.logKey, secondary, sc.set) && ok
	}
	ok = b.applyOptionUpdates(options) && ok

	// A live change can alter the option-group SET -- most often switching to a model whose
	// reasoning-effort variants differ surfaces, drops, or re-levels the effort axis (folded
	// from the set_config_option responses above). The frontend rebuilds its option-group
	// catalog only from statusChange events, and neither the per-axis write replies nor -- for
	// OpenCode/Copilot/Cursor, which emit no config_option_update notification -- the server
	// carries one, so push a status refresh here. Scoped to this live entry point: the
	// reapply/ClearContext path broadcasts its own refresh (see applySessionRefresh and the
	// post-handshake BroadcastStatusActive), so the model setter itself stays broadcast-free.
	b.mu.Lock()
	optionGroupsChanged := b.options.structureGen != structureGenBefore
	sessionID := b.sessionID
	b.mu.Unlock()
	// b.sink is always set on a live agent; the nil check guards bare-acpBase unit tests that
	// drive UpdateSettings without wiring a sink (a structural change would otherwise panic).
	if optionGroupsChanged && b.sink != nil {
		b.sink.BroadcastStatusActive(sessionID)
	}
	return ok
}

// applySessionRefresh parses the session response once, refreshes availableModels from
// both model channels, updates b.model (normalized via modelIDNormalizer) and the
// secondary field (permission mode / primary agent, derived from modeChannel via
// secondaryChannel), then logs and persists. Both the model list AND the secondary-option
// list (available modes / primary agents) are refreshed -- not just the current ids -- so
// a ClearContext'd session whose available options differ from the original handshake
// reflects the new lists instead of going stale; the current selection is then resolved
// against the refreshed list via reconcileCurrentOptionID. How the secondary value is
// persisted (its own permissionMode arg vs. a provider-option key) is derived from b.modeChannel,
// so every ACP family shares this one body -- callers pass no per-family parameters.
func (b *acpBase) applySessionRefresh(resp json.RawMessage) {
	sc := b.secondaryChannel()
	// Derive the available-model list (the union of both channels) and the current
	// model/secondary id from a single parse of resp.
	var model, secondaryVal string
	var models []*ModelInfo
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
	// All four refresh steps run under one lock so a racing config_option_update can't
	// pair a freshly-changed option with a stale model/secondary in the persisted row.
	b.mu.Lock()
	b.refreshModelsLocked(models, modelsFieldInfos, model, b.modelIDNormalizer)
	sc.refreshLocked(modes, secondaryVal, configOptions)
	// Refresh the mutable option groups from the new session, next to the mapped channels.
	// The session response's configOptions are a complete snapshot, so this is a no-op for
	// providers that surface no option (Cursor) and correctly drops any option the new
	// session no longer reports; an empty configOptions (inventory not yet resolved) leaves
	// the stored options untouched. The KeepingStored variant keeps an option value at
	// what reapplyOptions just re-pushed (the user's choice) rather than reverting it
	// to this captured snapshot's server default, which predates the re-push.
	_, optionListChanged := b.applyOptionGroupsKeepingStoredLocked(configOptions)
	snapshotModel, snapshotSecondary, persistMode, optionValues := b.snapshotRefreshForPersistLocked(sc)
	sessionID := b.sessionID
	b.mu.Unlock()
	slog.Info("acp agent settings refreshed from session",
		"provider", b.providerName,
		"agent_id", b.agentID,
		"model", snapshotModel,
		sc.logKey, snapshotSecondary,
	)
	b.sink.PersistSettingsRefresh(acpRefreshMap(snapshotModel, persistMode, optionValues))
	// A ClearContext refresh that changed only the option-group LIST (the new session
	// surfaces an option with different available values, but its current selection is
	// unchanged) leaves PersistSettingsRefresh a no-op: it merges option VALUES, which did
	// not change, so it neither persists the new catalog nor broadcasts it. Push a status
	// refresh directly so the frontend's option groups don't go stale -- mirroring the
	// list-only branch of handleACPConfigOptionUpdate. When a value DID change,
	// PersistSettingsRefresh already broadcast the live catalog; on ClearContext the values
	// are kept (re-pushed before this refresh), so this seldom double-fires.
	if optionListChanged {
		b.sink.BroadcastStatusActive(sessionID)
	}
}

// refreshModelsLocked replaces the available-model catalog and current model from a
// parsed session response. An empty model list leaves availableModels AND modelsFieldInfos
// at the prior session's values together (so they never desync and a later
// config_option_update re-union doesn't drop models-field-only entries); an empty current
// model leaves b.model untouched (kept from the reapplySettings re-push just before this).
// Caller holds b.mu.
func (b *acpBase) refreshModelsLocked(models []*ModelInfo, modelsFieldInfos []acpModelInfo, model string, normalizeModel func(string) string) {
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

// refreshLocked rebuilds the secondary-option list (permission modes or primary agents) from
// the native modes channel and resolves *sc.field against it the same way the handshake does:
// adopt a valid reported value, keep the still-selectable stored selection (re-pushed by
// reapplySettings just before this), else re-seed to default-or-first. An empty rebuild keeps
// the prior list. A configOptions `mode` override (Copilot/Goose/Cursor permission mode, or
// OpenCode/Kilo primary agent) wins when present. Caller holds the owning acpBase.mu (the
// closures touch acpBase fields).
func (sc acpSecondaryChannel) refreshLocked(modes []acpModeInfo, reportedSecondary string, configOptions []acpConfigOption) {
	sc.rebuild(modes, reportedSecondary)
	reportedSecondary = sc.hiddenFilter(reportedSecondary)
	if resolved := reconcileCurrentOptionID(*sc.available, reportedSecondary, *sc.field); resolved != "" {
		*sc.field = resolved
	}
	// The configOptions override is applied last so it wins over the modes-channel value,
	// matching applyHandshakeMode. sc.field aliases b.permissionMode / b.currentPrimaryAgent,
	// so the snapshot reflects it. nil for the unmapped channel (no override).
	if sc.syncConfigOverride != nil {
		sc.syncConfigOverride(configOptions)
	}
}

// snapshotRefreshForPersistLocked captures the model, the value to log/persist as the
// secondary, the PersistSettingsRefresh mode arg, and the option values -- all under the
// refresh lock, so a racing config_option_update can't pair a freshly-changed option with
// a stale model/secondary in the persisted row. The primary-agent providers carry the
// secondary in the option values (primaryAgentOptions, which the options overlay onto); the
// permission-mode providers carry it in PersistSettingsRefresh's own mode arg. Caller holds b.mu.
func (b *acpBase) snapshotRefreshForPersistLocked(sc acpSecondaryChannel) (snapshotModel, snapshotSecondary, persistMode string, optionValues map[string]string) {
	snapshotModel = b.model
	snapshotSecondary = *sc.field
	optionsBase, persistMode := sc.persistShape(snapshotSecondary)
	optionValues = b.options.mergeOptionValues(optionsBase)
	return snapshotModel, snapshotSecondary, persistMode, optionValues
}

// primaryAgentOptions builds the option-values map carrying the primary-agent
// selection for OpenCode/Kilo, returning nil (not an empty map) when the agent
// is empty. nil tells PersistSettingsRefresh to keep the stored option values, whereas
// a non-nil map{primaryAgent: ""} would marshal to "{}" (marshalOptions
// drops empty values) and wipe the stored primary agent. Used by every path
// that persists or reports primary-agent options so they can't diverge.
func primaryAgentOptions(agent string) map[string]string {
	if agent == "" {
		return nil
	}
	return map[string]string{OptionIDPrimaryAgent: agent}
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
func (b *acpBase) buildPrimaryAgentOptions(modes []acpModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	normalized := make([]acpModeInfo, len(modes))
	copy(normalized, modes)
	for i := range normalized {
		normalized[i].Name = normalizeOptionName(normalized[i].Name, normalized[i].ID)
	}
	return buildACPModes(normalized, currentModeID, b.primaryAgentHiddenFilter)
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
	if reported != "" && hasACPOption(available, reported) {
		return reported
	}
	if stored != "" && hasACPOption(available, stored) {
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
// response handler. The shared send/queue core behind doSendACPPrompt.
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
	provider       leapmuxv1.AgentProvider                    // registry key; lets acpStart seed b.secondaryFallback from the provider's registration
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
func acpStart[T any](ctx context.Context, opts Options, sink OutputSink, spec acpStartSpec[T]) (_ Agent, retErr error) {
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
	b.model = opts.Model()
	// Default prompt sender shared by every ACP provider; a provider may override
	// it in configure.
	b.promptFunc = b.doSendACPPrompt
	// Default settings-lifecycle hooks shared by every mode-bearing ACP provider:
	// reapply on relaunch/ClearContext and refresh from a session response both derive
	// the secondary axis + model writer from modeChannel/modelSetter, so one body serves
	// every family. A provider with no modes/configOptions channel (Reasonix) nils these
	// out in configure; the ClearContext path nil-guards both before calling.
	b.reapplySettings = b.reapplyModelAndSecondary
	b.refreshFromSession = b.applySessionRefresh
	if spec.configure != nil {
		spec.configure(a)
	}
	// Seed the secondary-axis fallback from the provider's registration (configure has set
	// b.modeChannel by now) so each provider names its fallback list once -- in its registerXxx
	// call -- instead of also in configure. A provider may still set b.secondaryFallback in
	// configure to override; the unmapped channel (Reasonix) has none, so this leaves it nil.
	if b.secondaryFallback == nil {
		b.secondaryFallback = registeredSecondaryFallback(spec.provider, b.modeChannel)
	}

	if err := b.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	// The subprocess is running now. Any failure past this point must tear it
	// down -- cancel kills the ctx-bound child -- because acpStart returns no
	// Agent on error, so the caller never gets a handle to Stop() it.
	defer func() {
		if retErr != nil {
			cancel()
		}
	}()

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

// Compile-time proof that every concrete ACP agent implements Agent. acpStart is
// generic over T and can only assert this at runtime (any(a).(Agent)); these
// guards turn a dropped or renamed method into a build error rather than a
// launch-time "does not implement Agent".
var (
	_ Agent = (*CursorCLIAgent)(nil)
	_ Agent = (*CopilotCLIAgent)(nil)
	_ Agent = (*GooseCLIAgent)(nil)
	_ Agent = (*KiloAgent)(nil)
	_ Agent = (*OpenCodeAgent)(nil)
	_ Agent = (*ReasonixAgent)(nil)
)

// OptionGroups returns one ACP provider's configuration axes as option groups:
// the model group, then -- for a provider with a secondary axis (permission mode or
// primary agent) -- that mapped group carrying its current value, then any mutable option
// groups the server surfaced. A model-only provider (Reasonix, modeChannelUnmapped) omits
// the secondary group. One body serves every ACP family; the axis specifics come from
// secondaryChannel and the per-provider secondaryFallback, so no provider overrides this.
func (b *acpBase) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.mu.Lock()
	defer b.mu.Unlock()
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := modelOptionGroup(b.availableModels, b.model, effortSubGroups); mg != nil {
		groups = append(groups, mg)
	}
	if grp := b.secondaryOptionGroupLocked(); grp != nil {
		groups = append(groups, grp)
	}
	return append(groups, b.options.groups...)
}

// secondaryOptionGroupLocked builds the mapped secondary-axis group (permission mode or
// primary agent) with its live current value, falling back to the static secondaryFallback
// list before the session reports a catalog. Returns nil for a model-only provider. Caller
// holds b.mu.
func (b *acpBase) secondaryOptionGroupLocked() *leapmuxv1.AvailableOptionGroup {
	if b.modeChannel == modeChannelUnmapped {
		return nil
	}
	sc := b.secondaryChannel()
	options := *sc.available
	if len(options) == 0 {
		options = b.secondaryFallback
	}
	return secondaryGroup(sc.secondaryAxis, options, *sc.field)
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
func buildACPModels(models []acpModelInfo, currentModelID string, normalize func(string) string) []*ModelInfo {
	if normalize != nil {
		currentModelID = normalize(currentModelID)
	}
	result := make([]*ModelInfo, 0, len(models))
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
		result = append(result, &ModelInfo{
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
	// acpConfigOptionCategoryThoughtLevel marks a reasoning-effort axis (OpenCode/Kilo
	// "effort", Copilot "reasoning_effort"); its options are reordered strongest-first.
	acpConfigOptionCategoryThoughtLevel = "thought_level"
)

// isSelectableConfigOption reports whether a config option is a value-list selector
// we can render. The ACP spec defines `type` as "select"-only today; an empty type
// is treated as "select" for back-compat (every provider we ship omits it). Any
// other (future) type is ignored defensively so an unknown widget kind never reaches
// a model/mode/option picker that only understands a list of values.
func isSelectableConfigOption(o acpConfigOption) bool {
	return o.Type == "" || o.Type == "select"
}

// acpSelectableConfigOptionByID returns the SELECTABLE config option with the given id. It resolves
// a (non-conforming) daemon reporting the SAME id more than once to a STABLE choice -- the
// content-smallest match (acpConfigOptionContentLess) -- rather than whichever the server listed
// first, so the claimed model/mode axis can't flip between payloads sent in different orders.
// Mirrors acpConfigOptionByCategory's lowest-id determinism for the category pass. It also scans
// PAST a non-selectable first match to a selectable later one. ("", false) when no selectable match.
func acpSelectableConfigOptionByID(options []acpConfigOption, id string) (acpConfigOption, bool) {
	found := false
	var best acpConfigOption
	for _, option := range options {
		if option.ID != id || !isSelectableConfigOption(option) {
			continue
		}
		if !found || acpConfigOptionContentLess(option, best) {
			best, found = option, true
		}
	}
	return best, found
}

// acpConfigOptionContentLess is a stable total order over config options, used ONLY to break a
// duplicate-id tie deterministically -- two options sharing an id is a spec violation, so this
// carries no semantic meaning; it just makes the resolution slice-order-independent. Orders by
// CurrentValue, then by the option values joined in sorted (order-independent) order.
func acpConfigOptionContentLess(a, b acpConfigOption) bool {
	if a.CurrentValue != b.CurrentValue {
		return a.CurrentValue < b.CurrentValue
	}
	return acpConfigOptionValuesKey(a) < acpConfigOptionValuesKey(b)
}

// acpConfigOptionValuesKey joins an option's offered values in sorted order into a stable key, so
// two duplicate-id options are ordered the same regardless of the order the server lists either's
// values in.
func acpConfigOptionValuesKey(o acpConfigOption) string {
	vals := make([]string, 0, len(o.Options))
	for _, v := range o.Options {
		vals = append(vals, v.Value)
	}
	slices.Sort(vals)
	return strings.Join(vals, "\x00")
}

// acpConfigOptionByCategory returns the selectable config option for a semantic
// category, in two passes: first the option whose `category` matches (the ACP
// spec's intended signal), then -- for the providers we ship today, which omit
// `category` and use the literal well-known id -- the option whose `id` matches
// `fallbackID`. Both passes skip non-selectable options (see
// isSelectableConfigOption), so an unknown widget type is ignored rather than
// dispatched as a model/mode. Returns the matched option and true, or a zero option
// and false when neither pass finds a selectable match.
//
// BOTH passes resolve a (pathological) duplicate deterministically so the claimed axis can't flip
// between payloads the server lists in different orders: the category pass picks the LOWEST id among
// same-category matches -- breaking an exact-id tie (two options sharing BOTH category and id) by the
// content-smallest occurrence -- and the id-fallback picks the content-smallest among same-id matches
// (acpSelectableConfigOptionByID). Mirrors thoughtLevelConfigOptionID's sorted resolution.
func acpConfigOptionByCategory(options []acpConfigOption, category, fallbackID string) (acpConfigOption, bool) {
	found := false
	var best acpConfigOption
	for _, option := range options {
		if option.Category != category || !isSelectableConfigOption(option) {
			continue
		}
		if !found || option.ID < best.ID || (option.ID == best.ID && acpConfigOptionContentLess(option, best)) {
			best, found = option, true
		}
	}
	if found {
		return best, true
	}
	return acpSelectableConfigOptionByID(options, fallbackID)
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
func (b *acpBase) buildModels(infos []acpModelInfo, currentModelID string) ([]*ModelInfo, string) {
	models := buildACPModels(infos, currentModelID, b.modelIDNormalizer)
	if b.modelIDNormalizer != nil {
		currentModelID = b.modelIDNormalizer(currentModelID)
	}
	if b.modelDecorator != nil {
		for _, m := range models {
			b.modelDecorator(m)
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
	// for option surfacing -- and it is already under the lock. The handshake snapshot is
	// the authoritative initial state, so the payload's CurrentValue wins.
	b.applyOptionGroupsLocked(handshake.ConfigOptions)
}

// trySetStartupModel applies a requested model during startup, best-effort. It is
// a no-op when the request is empty or already matches the server's current
// model. A failure is logged but NON-FATAL: the agent keeps the server's current
// model and stays usable. This is intentional -- some ACP agents do not advertise
// a model list and accept arbitrary ids, so a rejected model must not abort an
// otherwise-healthy session whose other settings were already applied. The model
// is written via effectiveSetModel so the "how to write a model" decision (Cursor's
// wire-mapped setCursorModel vs the base setModel) lives in exactly one place.
func (b *acpBase) trySetStartupModel(requested string) {
	if requested == "" {
		return
	}
	b.mu.Lock()
	current := b.model
	b.mu.Unlock()
	if requested == current {
		return
	}
	if err := b.effectiveSetModel()(requested); err != nil {
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
	return b.setSecondary(requested)
}

// applyHandshakeMode sets availableModes and the permission mode from a session
// handshake (under the lock), falling back to defaultMode when the server reports none.
// A `mode` config option overrides the permission mode read from the modes channel only
// for a provider that consumes it (modeChannelPermissionMode -- Copilot/Goose/Cursor);
// for an unmapped provider it is left to applyOptionGroupsLocked to
// surface as a option group, matching the runtime and ClearContext paths so the option
// resolves the same way at every seam instead of being applied as the permission mode here
// but surfaced uniformly there.
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

// applySecondaryStartup runs the post-handshake startup sequence shared by both ACP
// secondary families: apply the handshake models, configure the family's secondary axis
// (fatal on rejection -- the agent is torn down and a startup error returned), then push
// the requested model LAST and best-effort, and finally apply the remaining startup
// options. Keeping the order here in one body means the load-bearing invariant -- the
// model is applied last so a rejected model id cannot undo the secondary axis or abort an
// otherwise-healthy session -- can't drift between the two families. configureSecondary
// wires the family-specific secondary configuration (permission mode or primary agent),
// which is the only step that differs.
func (b *acpBase) applySecondaryStartup(handshake *acpSessionResult, opts Options, requestedModel string, configureSecondary func() error) error {
	b.applyHandshakeModels(handshake)
	if err := configureSecondary(); err != nil {
		b.stopAndWait()
		return b.formatStartupError(acpMethodSessionSetMode, err)
	}
	b.trySetStartupModel(requestedModel)
	b.applyStartupOptions(opts)
	return nil
}

// applyPermissionModeStartup runs the post-handshake startup sequence for the
// permission-mode providers (Copilot, Goose, Cursor): write the mode channel from the
// handshake and push the requested permission mode. Cursor passes its normalized model
// id; trySetStartupModel routes through effectiveSetModel, which picks Cursor's
// wire-mapping setCursorModel automatically. See applySecondaryStartup for the shared
// model-last ordering.
func (b *acpBase) applyPermissionModeStartup(handshake *acpSessionResult, opts Options, defaultMode, requestedModel string) error {
	return b.applySecondaryStartup(handshake, opts, requestedModel, func() error {
		b.applyHandshakeMode(handshake, defaultMode)
		return b.applyStartupPermissionMode(opts.PermissionMode())
	})
}

// applyPrimaryAgentStartup runs the post-handshake startup sequence for the primary-agent
// providers (OpenCode, Kilo): configure the available primary agents and apply the
// requested one from the persisted options. The primary-agent mirror of
// applyPermissionModeStartup; the two differ only in this secondary configuration. The
// fallback list is read from b.secondaryFallback (set in the provider's configure step,
// which runs before this), so each provider sources its primary-agent list exactly once.
func (b *acpBase) applyPrimaryAgentStartup(handshake *acpSessionResult, opts Options, defaultAgent string) error {
	return b.applySecondaryStartup(handshake, opts, opts.Model(), func() error {
		return b.configurePrimaryAgents(handshake.Modes, handshake.CurrentModeID, opts.Get(OptionIDPrimaryAgent), b.secondaryFallback, defaultAgent)
	})
}

// handleACPConfigOptionUpdate processes a config_option_update notification. The
// model channel is handled uniformly for every ACP provider; the configOptions
// `mode` select is applied as the permission mode for modeChannelPermissionMode
// providers (Copilot/Goose/Cursor) or the primary agent for modeChannelPrimaryAgent
// providers (OpenCode/Kilo); any unmapped option is surfaced as a mutable option
// group. All
// channels mutate under a single lock so a concurrent settings read can never observe
// a half-applied update. Broadcasts happen after the lock is released.
//
// A setting change (model, primary agent, or an option value) persists and broadcasts
// the full settings exactly once via broadcastSettingsRefresh; when only the
// permission mode changed, UpdatePermissionMode persists+broadcasts it together with
// the chat notification. A mode change that rides alongside a model/primary-agent/
// option change emits only the chat notification, since broadcastSettingsRefresh
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
	// Apply the secondary axis (permission mode or primary agent) through the resolved channel,
	// so this path no longer hand-picks which family-specific Locked method to call -- that
	// distinction lives in secondaryChannel(). The returned value/changed are then mapped onto
	// the family the downstream switch routes on (mode -> Notify/UpdatePermissionMode, primary
	// agent -> full refresh).
	sc := b.secondaryChannel()
	var secondaryValue string
	var secondaryChanged bool
	if sc.syncConfigOverride != nil {
		var secondaryListChanged bool
		secondaryValue, secondaryChanged, secondaryListChanged = sc.syncConfigOverride(options)
		listChanged = listChanged || secondaryListChanged
	}
	mode := secondaryValue
	modeChanged := secondaryChanged && sc.routesAsPermissionMode()
	primaryAgentChanged := secondaryChanged && sc.routesAsPrimaryAgent()
	// Surface/sync any unmapped config option (mutable option groups). A
	// option value change persists via broadcastSettingsRefresh (below); a
	// option list-only change rides the status-refresh branch. A server-initiated update is
	// authoritative, so the payload's CurrentValue wins.
	optionValueChanged, optionListChanged := b.applyOptionGroupsLocked(options)
	sessionID := b.sessionID
	b.mu.Unlock()

	switch {
	case modelChanged || primaryAgentChanged || optionValueChanged:
		// A model, primary-agent, or option-value change persists+broadcasts the full
		// settings in one StatusChange (which re-fetches the live model list and carries
		// the live mode and options), so the frontend reflects the runtime switch
		// immediately. A option value must persist via broadcastSettingsRefresh -- not
		// BroadcastStatusActive -- so the new selection survives in the options column.
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
	case listChanged || optionListChanged:
		// An available list changed (models, modes, primary agents, or a config option
		// set) but no current selection did. broadcastSettingsRefresh would no-op
		// (PersistSettingsRefresh skips when model/mode/options are unchanged), so
		// broadcast a status refresh directly -- its StatusChange re-fetches the live
		// model list and option groups, surfacing the new options.
		b.sink.BroadcastStatusActive(sessionID)
	}
}

// applyConfigOptionModelsLocked refreshes availableModels and the current model
// from the `model` select of a configOptions payload, applying modelIDNormalizer
// and modelsDecorator. It returns whether the current model changed and whether
// the available-model list changed. The caller must hold b.mu. This is the shared
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
	if len(models) > 0 && !modelInfosEqual(b.availableModels, models) {
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
		refresh[OptionIDModel] = model
	}
	if mode != "" {
		refresh[OptionIDPermissionMode] = mode
	}
	return refresh
}

// broadcastSettingsRefresh persists and broadcasts the agent's current settings.
// It reads the live model/mode/primary-agent state, so it serves both permission-
// mode providers (currentPrimaryAgent == "" -> nil option values) and primary-agent
// providers (permissionMode == "" -> the stored mode is preserved by the sink).
func (b *acpBase) broadcastSettingsRefresh() {
	b.mu.Lock()
	model := b.model
	mode := b.permissionMode
	// Carry the live option values too: a model/primary-agent change that did not
	// also touch the options must still re-include them or the refresh would not
	// reflect them.
	optionValues := b.options.mergeOptionValues(primaryAgentOptions(b.currentPrimaryAgent))
	b.mu.Unlock()
	b.sink.PersistSettingsRefresh(acpRefreshMap(model, mode, optionValues))
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

// sendSessionRPC sends an ACP session/* request under withSessionID, injecting the current
// sessionId alongside extraParams, and returns the raw response after unwrapping a JSON-RPC
// result error. It centralizes the three things every session RPC must do -- hold the
// withSessionID lock discipline, marshal {sessionId, ...}, and unwrap jsonRPCResultError --
// so a new session RPC can't forget any of them. Callers that need the response body (e.g.
// set_config_option folding the refreshed configOptions) use the returned RawMessage; callers
// that only care about success discard it. (cancelSession is a notification with no response,
// so it does not go through here.)
func (b *acpBase) sendSessionRPC(method string, extraParams map[string]interface{}) (json.RawMessage, error) {
	var out json.RawMessage
	err := b.withSessionID(func(sessionID string) error {
		params := make(map[string]interface{}, len(extraParams)+1)
		params["sessionId"] = sessionID
		for k, v := range extraParams {
			params[k] = v
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		resp, err := b.sendRequest(method, json.RawMessage(raw), b.APITimeout())
		if err != nil {
			return err
		}
		if err := jsonRPCResultError(resp); err != nil {
			return err
		}
		out = resp
		return nil
	})
	return out, err
}

// setModelViaConfigOption writes the model to the daemon via ACP's
// session/set_config_option (configId "model"), WITHOUT touching b.model. Callers
// store the local model themselves -- setModel stores the same id, while Cursor's
// setCursorModel stores the normalized (display) id rather than the wire id. Keeping
// the field write out of the RPC avoids a window where b.model briefly holds the wire
// id (e.g. "default[]") that a concurrent OptionGroups() read could observe and
// persist.
//
// We use set_config_option rather than the experimental session/set_model because the
// ACP spec requires set_config_option's response to carry the full refreshed
// configOptions, whereas session/set_model returns only _meta. Folding that response
// surfaces (or drops) a model-dependent option group -- the reasoning-effort axis that
// OpenCode/Kilo/Goose/Copilot each gate on the current model's own variants -- the
// instant the model changes. The old set_model write left the effort group stale or
// missing because its empty response gave nothing to fold. Every ACP agent we drive
// accepts configId "model" here (OpenCode, Kilo, Goose, Copilot, Cursor); Reasonix
// pins its model at launch and never reaches this path.
func (b *acpBase) setModelViaConfigOption(wireModel string) error {
	resp, err := b.sendSessionRPC(acpMethodSessionSetConfigOption, map[string]interface{}{
		"configId": acpConfigOptionIDModel,
		"value":    wireModel,
	})
	if err != nil {
		return err
	}
	// Fold the refreshed configOptions so a model-dependent option group is surfaced or
	// dropped immediately. The model and mode selects in the payload are claimed
	// channels applyOptionGroupsLocked skips, so this only touches the mutable option
	// groups (effort / reasoning_effort / thinking_effort); the model field stays the
	// caller's responsibility. A spec-compliant agent always returns the options; an
	// off-spec empty response simply leaves the prior groups untouched. Surfacing the new
	// group to the frontend (a status refresh when the group SET changed) is the live
	// UpdateSettings caller's job; the reapply/ClearContext caller broadcasts its own.
	options := parseACPConfigOptions(resp)
	if len(options) > 0 {
		b.mu.Lock()
		b.applyOptionGroupsLocked(options)
		b.mu.Unlock()
	}
	// A model that newly surfaces a reasoning-effort axis often defaults it to "none" --
	// leaving the model reasoning-disabled the moment it is selected. Raise that default to
	// a real level (see raiseEffortOffNone). Runs after the fold so it reads the freshly
	// resolved current value, and pushes its own set_config_option so the daemon and UI agree.
	b.raiseEffortOffNone(options)
	return nil
}

// setModel writes the model via session/set_config_option and updates the local field.
func (b *acpBase) setModel(model string) error {
	if err := b.setModelViaConfigOption(model); err != nil {
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
	_, err := b.sendSessionRPC(acpMethodSessionSetMode, map[string]interface{}{"modeId": modeID})
	return err
}

// setConfigOption writes a mutable config option (one with no
// dedicated set_model/set_mode channel -- e.g. OpenCode/Kilo "effort", Copilot
// "reasoning_effort"/"allow_all") via ACP's session/set_config_option, then folds the
// refreshed configOptions the server returns back into the local option state so the
// next OptionGroups() read reflects the new value. configID must be a currently
// surfaced config option id.
func (b *acpBase) setConfigOption(configID, value string) error {
	return b.setConfigOptionGuarded(configID, value, nil)
}

// setConfigOptionGuarded is setConfigOption with an optional last-moment precondition. stillWanted
// (when non-nil) is evaluated under the SAME b.mu acquisition as the known/offered gates -- the
// tightest point before the wire send -- so a caller whose write is only valid while the live state
// still holds (raiseEffortOffNone: "the effort axis is still at the daemon's none/off default") can
// abort if a concurrent fold already moved it. handleACPConfigOptionUpdate folds under b.mu, so such
// a fold either lands before this read (the precondition sees it and skips) or after the send (the
// daemon's own ordering resolves the two writes); only the async RPC itself remains outside the lock,
// an irreducible window we deliberately do not close by holding b.mu across an RPC. A false
// precondition is a no-op success.
func (b *acpBase) setConfigOptionGuarded(configID, value string, stillWanted func() bool) error {
	// Gate on the advertised-option set (every option the server has advertised) rather than
	// the surfaced-option values (only those with a concrete current value surfaced): an option
	// the server advertised with an empty current is pushable so its persisted preference
	// can be re-applied, even though it isn't yet surfaced as a group.
	b.mu.Lock()
	known := b.options.known.has(configID)
	offered := b.options.offersValue(configID, value)
	// Evaluate the precondition under this same lock so it can't be invalidated between the check
	// and the gates below by a concurrent b.mu holder.
	wanted := stillWanted == nil || stillWanted()
	b.mu.Unlock()
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
			"provider", b.providerName, "agent_id", b.agentID, "option", configID, "value", value)
		return nil
	}

	resp, err := b.sendSessionRPC(acpMethodSessionSetConfigOption, map[string]interface{}{
		"configId": configID,
		"value":    value,
	})
	if err != nil {
		return err
	}
	// The response carries the refreshed configOptions; fold the option ones back in so
	// the new current value rides along in the option values on the next read (the payload is
	// authoritative, so authoritativePayload). A server that accepted the write but returned
	// no configOptions (off-spec, but possible) leaves no snapshot to fold: record the
	// value we just wrote optimistically rather than keeping the stale prior value, since
	// the set succeeded (the result-error unwrap in sendSessionRPC passed) and the value is
	// therefore what the session is now running -- otherwise applySettingsLive's readback
	// would persist the stale value and revert the user's choice.
	b.mu.Lock()
	if options := parseACPConfigOptions(resp); len(options) > 0 {
		b.applyOptionGroupsLocked(options)
	} else {
		b.options.recordOptimistic(configID, value)
	}
	b.mu.Unlock()
	return nil
}

// withOptionWriteBatch owns the optionWriteMu->b.mu lock ordering and the snapshot
// clone that every config-option write batch needs -- a discipline documented at
// length on optionWriteMu and otherwise easy to subtly re-implement wrong (an inverted
// lock order deadlocks; a missing clone races the reader goroutine that folds a
// server-initiated config_option_update mid-batch). optionWriteMu is held for the whole
// batch (serializing it against another batch); b.mu is released before fn runs because
// fn's per-id RPCs re-lock it. fn receives consistent snapshots of the current option
// values and the known-option id set, and must iterate those rather than the live maps.
func (b *acpBase) withOptionWriteBatch(fn func(values map[string]string, known []string)) {
	b.optionWriteMu.Lock()
	defer b.optionWriteMu.Unlock()
	b.mu.Lock()
	values := maps.Clone(b.options.values)
	known := b.options.known.keys()
	b.mu.Unlock()
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
func (b *acpBase) forEachOption(decide func(id, current string) (value string, want bool)) bool {
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
func (b *acpBase) applyConfigOption(id, value string) bool {
	return b.applyConfigOptionGuarded(id, value, nil)
}

// applyConfigOptionGuarded is applyConfigOption with an optional last-moment precondition threaded
// through to setConfigOptionGuarded (see there). Used by raiseEffortOffNone so its rank-0 re-check
// runs under the write's own b.mu acquisition rather than an earlier, wider-windowed one.
func (b *acpBase) applyConfigOptionGuarded(id, value string, stillWanted func() bool) bool {
	return acpApplySetting(b.providerName, b.agentID, id, value, func(val string) error {
		return b.setConfigOptionGuarded(id, val, stillWanted)
	})
}

// applyOptionUpdates writes every mutable config-option value present
// in a sparse settings update whose value differs from the current selection, via
// session/set_config_option. Returns false if any write failed; the agent stays
// usable (a rejected option keeps its prior value), and the caller treats false as
// "not fully applied". Ids are applied in sorted order for deterministic logging.
func (b *acpBase) applyOptionUpdates(options map[string]string) bool {
	return b.forEachOption(func(id, current string) (string, bool) {
		v, present := options[id]
		return v, present && v != current
	})
}

// reapplyOptions re-applies the user's stored config-option selections after a session/new
// (ClearContext), mirroring reapplyModelAndSecondary for the model/mode channels, so a user's
// effort / reasoning-effort / allow-all choice survives a context clear. `stored` is the
// snapshot reapplyModelAndSecondary captured BEFORE the model re-push.
func (b *acpBase) reapplyOptions(stored map[string]string) {
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
func (b *acpBase) applyStartupOptions(opts Options) {
	// A daemon may drive its reasoning-effort axis under a NON-"effort" id (Copilot
	// reasoning_effort, Goose thinking_effort, or a thought_level-categorized custom id), but the
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
				requested = opts.Get(OptionIDEffort)
			}
			if requested == "" || requested == values[id] {
				continue
			}
			if err := b.setConfigOption(id, requested); err != nil {
				slog.Warn("requested config option not applied; keeping current",
					"provider", b.providerName, "agent_id", b.agentID,
					"option", id, "requested", requested, "current", values[id], "error", err)
			}
		}
	})
}

// cancelSession sends a session/cancel notification.
func (b *acpBase) cancelSession() error {
	return b.withSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]interface{}{
			"sessionId": sessionID,
		})
		if err != nil {
			return fmt.Errorf("marshal cancel params: %w", err)
		}
		return b.sendNotification(acpMethodSessionCancel, json.RawMessage(params))
	})
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
	claimToken := b.sink.PersistControlRequest(id, content)
	b.sink.BroadcastControlRequest(id, content, claimToken)
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

// doSendACPPrompt sends a single ACP session/prompt RPC and processes the
// response. It is the default promptFunc for every ACP agent.
//
// No timeout on the RPC: the turn unblocks via response, process exit, or
// ctx cancel (the user interrupting). A wall-clock cap would just kill
// long-but-legitimate turns.
func (b *acpBase) doSendACPPrompt(content string, attachments []*leapmuxv1.Attachment) {
	b.sendPrompt(content, attachments,
		func(params json.RawMessage) (json.RawMessage, error) {
			return b.sendRequest(acpMethodSessionPrompt, params, 0)
		},
		b.handleACPPromptResponse,
	)
}
