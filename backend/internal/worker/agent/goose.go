package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Goose's server-driven ACP config-option ids (surfaced as mutable option groups,
// not static templates). Declared in KnownOptionIDs so a not-running agent validates
// them, matching the ids the live `session/set_config_option` channel reports. Goose
// has no well-known "effort" axis -- its reasoning axis is the config option "thinking_effort".
const (
	GooseConfigThinkingEffort = "thinking_effort"
	GooseConfigProvider       = "provider"
	gooseSteerNamespace       = "goose"
	gooseSteerMethod          = "_goose/unstable/session/steer"
	// The custom notification Goose sends only to a client that advertises
	// `customNotifications`, and the three updates it carries.
	gooseSessionUpdateMethod = "_goose/unstable/session/update"
	gooseUpdateUsage         = "usage_update"
	gooseUpdateStatusMessage = "status_message"
	// A NOTICE is a sentence the reader must see. The other variant, `progress`,
	// is live chrome that Goose says must not become history.
	gooseStatusNotice = "notice"
)

// GooseCLIAgent manages a single Goose CLI ACP process.
type GooseCLIAgent struct {
	acpBase
	gooseOutput map[string]gooseOutputState
}

type gooseOutputState struct {
	sequence uint64
	bytes    int64
	// bytesAreMinimum says the COUNT is a lower bound, which only Goose's own
	// truncation flag establishes: every chunk's length is added before the tail is
	// cut, so a local cap never makes the total approximate.
	bytesAreMinimum bool
	// tailLostBytes says the retained TAIL dropped earlier output, which both the
	// local cap and Goose's truncation cause. The two were one field, so trimming
	// an 8 KiB display window made the worker report an exact byte total as "at
	// least N" -- and `ProgressCounter` aggregates that flag across the agent, so
	// one long Goose call marked every concurrent tool's count approximate.
	tailLostBytes bool
	// tail is the text the chunks carried, joined. Goose sends a live_output
	// notification per CHUNK, so only this agent knows where the earlier ones
	// ended -- and the running row draws what the call has printed so far.
	tail string
}

// The longest live text one Goose call keeps between notifications.
//
// The sink caps the tail again before it broadcasts. This cap is here so the
// joined string cannot grow without limit inside the agent, for a command that
// prints for minutes.
const gooseLiveOutputLimit = 8192

func (a *GooseCLIAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	method, active, sessionID, runID := a.steerMethod, a.promptActive, a.sessionID, a.steerRunID
	a.mu.Unlock()
	if method == "" {
		return ErrSteeringUnsupported
	}
	if !active || runID == "" {
		return ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]interface{}{
		"sessionId":     sessionID,
		"expectedRunId": runID,
		"prompt":        buildACPPromptBlocks(content, classifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if _, err := a.sendRequest(method, params, a.APITimeout()); err != nil {
		if hasJSONRPCErrorCode(err, -32600, -32602) {
			return ErrNoActiveTurn
		}
		return classifyJSONRPCDeliveryError(method, err)
	}
	a.mu.Lock()
	stillActive := a.promptActive && a.steerRunID == runID
	a.mu.Unlock()
	if !stillActive {
		return ErrNoActiveTurn
	}
	return nil
}

// StartGooseCLI starts a Goose CLI ACP agent process and performs the handshake.
func StartGooseCLI(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[GooseCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		providerName: "goose",
		binaryName:   "goose",
		baseArgs:     []string{"acp"},
		newAgent:     func() *GooseCLIAgent { return &GooseCLIAgent{} },
		base:         func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *GooseCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			// Smart Approve is Goose's safe new-session mode, so it leads every rebuilt
			// list and is the mode the group badges as its default.
			a.preferredFirstMode = contracts.GooseModeSmartApprove
			// Goose's reasoning-effort axis is the convention id "thinking_effort", not the
			// well-known "effort" -- declare it so the env-effort override maps onto it.
			a.effortConfigID = GooseConfigThinkingEffort
			// Subagent tool-request observations: Goose surfaces tool REQUESTS
			// (never results) over ACP via _meta.toolNotification, so the hook
			// runs on tool_call_update. The spawn tool_call's final update
			// closes the registry row. The spawn tool_call itself carries
			// _meta.goose.toolCall {toolName:"delegate", extensionName:"summon"}.
			a.subagentFromToolCall = gooseSubagentFromToolCall
			a.subagentFromToolCallUpdate = gooseSubagentFromToolCallUpdate
			a.toolOutput = a.gooseToolOutput
			a.toolNotification = a.observeGooseToolNotification
			// Goose sends its live status and its usage totals ONLY to a client
			// that asks for them, on `_goose/unstable/session/update`. Without the
			// advertisement the counters stayed empty and every status line was
			// lost. Verified against goose 1.50.1: the handshake accepts the flag
			// and answers with its own `_meta.goose` capabilities.
			a.clientCapabilityMeta = map[string]any{
				gooseSteerNamespace: map[string]any{"customNotifications": true},
			}
			a.extraMethod = a.handleGooseExtraMethod
			a.toolOutputComplete = a.clearGooseToolOutput
			a.sessionMetadataHandler = a.captureSteerRunID
			a.advertisedSteerMethod = func(response []byte) string {
				return parseACPAdvertisedMethod(response, gooseSteerNamespace, gooseSteerMethod)
			}
		},
		afterHandshake: func(a *GooseCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, contracts.GooseModeAuto, opts.Model())
		},
	})
}

// captureSteerRunID reads the two steering fields Goose states on a
// `session_info_update`, and claims the update so nothing else has to read it.
//
// `activeRunId` is the run a steer must address; it arrives when a turn opens and
// again as null when the turn ends.
//
// `queuedSteer` ACKNOWLEDGES a steer LeapMux sent: a live `goose acp` probe recorded
// it as `{"messageId":"steer_...","runId":"run_..."}`, on its own update with no
// `activeRunId` beside it. It changes nothing here, because the steer request itself
// already answered and LeapMux knows the steer was accepted from that answer. It is
// READ so the update is claimed rather than falling through to be persisted as a row
// the browser then hides, and so the next reader finds the shape recorded rather than
// only the field name.
func (a *GooseCLIAgent) captureSteerRunID(updateType string, metadata map[string]json.RawMessage) bool {
	if updateType != "session_info_update" {
		return false
	}
	var goose map[string]json.RawMessage
	if json.Unmarshal(metadata[gooseSteerNamespace], &goose) != nil {
		return false
	}
	if queued, ok := goose["queuedSteer"]; ok {
		var steer struct {
			MessageID string `json:"messageId"`
			RunID     string `json:"runId"`
		}
		if json.Unmarshal(queued, &steer) == nil {
			slog.Debug("goose queued a steer", "agent_id", a.agentID, "message_id", steer.MessageID, "run_id", steer.RunID)
		}
		return true
	}
	rawRunID, ok := goose["activeRunId"]
	if !ok {
		return false
	}
	var runID string
	if string(rawRunID) != "null" && json.Unmarshal(rawRunID, &runID) != nil {
		return false
	}
	a.mu.Lock()
	a.steerRunID = runID
	a.mu.Unlock()
	return true
}

func (a *GooseCLIAgent) clearGooseToolOutput(toolCallID string) {
	a.mu.Lock()
	delete(a.gooseOutput, toolCallID)
	a.mu.Unlock()
}

// gooseToolNotification is the `_meta.toolNotification` envelope every live update
// of a running Goose call rides in.
//
// Goose declares FOUR types (`tool_notifications.rs`): `live_output` carries the
// shell text, `progress` carries a step count with the runtime's own sentence,
// `platform_event` carries an extension's announcement, and `message` carries a log
// line. Each arrives on a `tool_call_update` whose status is `in_progress`, so the
// shared classifier hides the row -- which is right for a row, and is also why the
// text inside it reaches the reader only when a hook below takes it.
type gooseToolNotification struct {
	Type   string `json:"type"`
	Params struct {
		// live_output
		Sequence  uint64 `json:"sequence"`
		Truncated bool   `json:"truncated"`
		Chunks    []struct {
			Output string `json:"output"`
		} `json:"chunks"`
		// progress
		Progress *float64 `json:"progress"`
		Total    *float64 `json:"total"`
		Message  string   `json:"message"`
		// platform_event
		Extension string `json:"extension"`
		EventType string `json:"event_type"`
	} `json:"params"`
}

func (a *GooseCLIAgent) gooseToolNotificationOf(update acpToolCallUpdateEnvelope) (gooseToolNotification, bool) {
	var meta struct {
		ToolNotification gooseToolNotification `json:"toolNotification"`
	}
	if json.Unmarshal(update.Meta, &meta) != nil || meta.ToolNotification.Type == "" {
		return gooseToolNotification{}, false
	}
	return meta.ToolNotification, true
}

// gooseProgressLine is the sentence one `progress` notification states, or "" when
// it states none.
//
// Goose sends the runtime's own `message` beside the counts, so the sentence leads
// and the counts qualify it -- `Scanned 3 of 10 directories (3/10)` reads worse than
// the sentence alone, which is why the counts are appended ONLY when the sentence is
// absent. A progress notification with neither says nothing a reader can use.
func gooseProgressLine(notification gooseToolNotification) string {
	if notification.Params.Message != "" {
		return notification.Params.Message
	}
	if notification.Params.Progress == nil {
		return ""
	}
	if notification.Params.Total != nil && *notification.Params.Total > 0 {
		return fmt.Sprintf("%s of %s",
			formatGooseCount(*notification.Params.Progress), formatGooseCount(*notification.Params.Total))
	}
	return formatGooseCount(*notification.Params.Progress)
}

// formatGooseCount drops the fraction of a whole number. Goose sends a float, and
// `3` reads better than `3.0` for a step count.
func formatGooseCount(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// observeGooseToolNotification reads the two live notifications that are neither
// shell output nor a log line, and reports whether it claimed the update.
//
// `progress` is the runtime's own sentence about a call that still runs, so it goes
// to the SAME ephemeral tail the shell output uses: the row draws it while the call
// runs and drops it when the result lands. Copilot's `progressMessage` takes that
// route for the same reason. Persisting one would write a row per tick.
//
// `platform_event` is an extension announcing that something happened outside this
// call -- an app created, a resource registered. That outlives the call, so it is a
// notification rather than a tail, and it is the one of the four that reaches the
// transcript.
func (a *GooseCLIAgent) observeGooseToolNotification(update acpToolCallUpdateEnvelope) bool {
	meta, ok := a.gooseToolNotificationOf(update)
	if !ok {
		return false
	}
	switch meta.Type {
	case "progress":
		line := gooseProgressLine(meta)
		if line == "" || update.ToolCallID == "" {
			return true
		}
		a.sink.ReportProgress(OutputTailProgress(update.ToolCallID, line, false))
		return true
	case "platform_event":
		if line := goosePlatformEventLine(meta); line != "" {
			a.sink.PersistLeapMuxNotification(map[string]interface{}{
				contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
				contracts.NotificationFieldText: line,
			})
		}
		return true
	}
	return false
}

// goosePlatformEventLine states which extension announced what.
//
// Goose puts `extension` and `event_type` in every platform event and lets the
// extension add its own fields, which this does NOT read: their names are the
// extension's, not Goose's, so a reader of this file cannot know what they mean.
func goosePlatformEventLine(meta gooseToolNotification) string {
	extension, event := meta.Params.Extension, meta.Params.EventType
	switch {
	case extension != "" && event != "":
		return fmt.Sprintf("%s: %s", extension, strings.ReplaceAll(event, "_", " "))
	case event != "":
		return strings.ReplaceAll(event, "_", " ")
	case extension != "":
		return fmt.Sprintf("%s sent an event", extension)
	default:
		return ""
	}
}

// gooseToolOutput reads one `live_output` notification into the count and the text
// it leaves behind -- ONE observation, under ONE lock acquisition, so the byte count
// and the tail a row draws can never come from two different states.
func (a *GooseCLIAgent) gooseToolOutput(update acpToolCallUpdateEnvelope) (acpToolOutputObservation, bool) {
	meta, ok := a.gooseToolNotificationOf(update)
	if !ok || meta.Type != "live_output" {
		return acpToolOutputObservation{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gooseOutput == nil {
		a.gooseOutput = make(map[string]gooseOutputState)
	}
	state := a.gooseOutput[update.ToolCallID]
	if meta.Params.Sequence <= state.sequence {
		return acpToolOutputObservation{}, false
	}
	state.sequence = meta.Params.Sequence
	for _, chunk := range meta.Params.Chunks {
		state.bytes = saturatingAdd(state.bytes, int64(len([]byte(chunk.Output))))
		state.tail += chunk.Output
	}
	// Keep the END, which is what a reader watches, and say that the start is gone.
	var clipped bool
	state.tail, clipped = ClipTailBytes(state.tail, gooseLiveOutputLimit)
	if clipped {
		state.tailLostBytes = true
	}
	if meta.Params.Truncated {
		state.bytesAreMinimum = true
		state.tailLostBytes = true
	}
	a.gooseOutput[update.ToolCallID] = state
	return acpToolOutputObservation{
		Total: state.bytes, TotalIsMinimum: state.bytesAreMinimum,
		Tail: state.tail, TailLost: state.tailLostBytes,
	}, true
}

// handleGooseExtraMethod claims the notifications Goose sends outside the Agent
// Client Protocol's own methods. It answers true for the ones it read, so the
// shared dispatcher does not persist them as raw rows.
func (a *GooseCLIAgent) handleGooseExtraMethod(line *parsedLine) bool {
	if line.Method != gooseSessionUpdateMethod {
		return false
	}
	a.handleGooseSessionUpdate(line.Params)
	return true
}

// handleGooseSessionUpdate reads one `_goose/unstable/session/update`.
//
// The three variants report different things and take different paths. A usage
// update is the session's own context and cost totals, which the meter reads. A
// status NOTICE is a sentence the reader must see, so it becomes a notification.
// Everything else is live chrome: Goose's own schema says a status message "is
// not conversation transcript content, and should not be persisted or replayed
// as history", and a per-message usage row states a cost the cumulative total
// beside it already carries -- a replay resends every one of them, so adding
// them up would count the turn twice.
func (a *GooseCLIAgent) handleGooseSessionUpdate(params json.RawMessage) {
	var notif struct {
		Update struct {
			SessionUpdate string   `json:"sessionUpdate"`
			Used          *int64   `json:"used"`
			ContextLimit  *int64   `json:"contextLimit"`
			InputTokens   int64    `json:"accumulatedInputTokens"`
			OutputTokens  int64    `json:"accumulatedOutputTokens"`
			Cost          *float64 `json:"accumulatedCost"`
			Status        struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("goose session update unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	update := notif.Update
	switch update.SessionUpdate {
	case gooseUpdateUsage:
		info := map[string]interface{}{}
		if update.Used != nil && *update.Used >= 0 {
			// Through the shared projection, which is the ONE place the four count
			// keys are written. Goose measures neither cache half, and both rows need
			// a ZERO rather than an absent key: an absent one blanks the row in the
			// breakdown instead of showing that the provider counted none. This site
			// spelled the keys itself and omitted both of them.
			usage := contextUsageMap(contextTokenCounts{
				Input:  update.InputTokens,
				Output: update.OutputTokens,
			})
			// The two conditional keys stay at the site, as contextTokenCounts states.
			usage[contracts.ContextUsageFieldContextTokens] = *update.Used
			if update.ContextLimit != nil && *update.ContextLimit > 0 {
				usage[contracts.ContextUsageFieldContextWindow] = *update.ContextLimit
			}
			info[contracts.SessionInfoKeyContextUsage] = usage
		}
		// The ACCUMULATED cost, which is a total rather than a delta -- a replay
		// resends the same total and cannot double it.
		if update.Cost != nil {
			info[contracts.SessionInfoKeyTotalCostUsd] = *update.Cost
		}
		if len(info) > 0 {
			a.sink.BroadcastSessionInfo(info)
		}
	case gooseUpdateStatusMessage:
		if update.Status.Type != gooseStatusNotice || update.Status.Message == "" {
			return
		}
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
			contracts.NotificationFieldText: update.Status.Message,
		})
	}
}

// fallbackGooseCLIModes lists Goose's modes in Goose's own order, then applies the same
// preferred-first rule the live catalog applies. Ordering here rather than hand-writing
// the result keeps the static fallback and every rebuilt list in agreement.
func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	modes := []*leapmuxv1.AvailableOption{
		{Id: contracts.GooseModeAuto, Name: "Auto"},
		{Id: contracts.GooseModeApprove, Name: "Approve"},
		{Id: contracts.GooseModeSmartApprove, Name: "Smart Approve"},
		{Id: contracts.GooseModeChat, Name: "Chat"},
	}
	orderModesPreferredFirst(modes, contracts.GooseModeSmartApprove)
	return modes
}

// gooseSubagentFromToolCall detects Goose's spawn tool_call by the structured
// _meta.goose.toolCall marker {toolName:"delegate", extensionName:"summon"}.
// This is the spawn detector (NOT title guessing). Registry-only here -- the
// child transcript is fed by the tool-request updates; the spawn tool_call
// itself just registers a running row with the spawn title.
func gooseSubagentFromToolCall(tc acpToolCallEnvelope) *acpSubagentObservation {
	if len(tc.Meta) == 0 {
		return nil
	}
	var meta struct {
		Goose struct {
			ToolCall struct {
				ToolName      string `json:"toolName"`
				ExtensionName string `json:"extensionName"`
			} `json:"toolCall"`
		} `json:"goose"`
	}
	if err := json.Unmarshal(tc.Meta, &meta); err != nil {
		return nil
	}
	tc2 := meta.Goose.ToolCall
	if tc2.ToolName != contracts.GooseSubagentTool || tc2.ExtensionName != contracts.GooseSubagentExtension {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = "Goose subagent"
	}
	return &acpSubagentObservation{
		RowKey: tc.ToolCallID,
		Title:  title,
		Status: bgtask.StatusRunning,
		Spawns: true,
		// Goose's delegate tool puts its task text in `instructions`, not `prompt`
		// (crates/goose/src/agents/platform_extensions/summon.rs). The child
		// transcript is created later, on the first forwarded tool request, so
		// applySubagentObservation holds this until then.
		Prompt: gooseDelegateInstructions(tc.RawInput),
	}
}

// gooseDelegateInstructions pulls the delegate call's task text out of the
// tool_call's rawInput. Goose fills raw_input from the tool arguments
// (acp/server/tool_calls/conversion.rs), so the delegate arguments arrive
// verbatim. Returns "" when absent.
func gooseDelegateInstructions(rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var in struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return ""
	}
	return in.Instructions
}

// gooseSubagentToolRequestType is the `data.type` that marks one subagent tool
// request inside Goose's logging metadata.
const gooseSubagentToolRequestType = "subagent_tool_request"

// gooseSubagentRequestedTool reads the name of the tool one subagent request asks
// for, or the empty string when the request states none.
//
// It indexes the `data` object with the generated constant instead of taking the key
// from a struct tag, which cannot hold one. Goose owns the word, and the browser
// draws the request row from the same object through the same contract table. A tag
// here would therefore keep the old word after a Goose release moved it: every
// request would fall back to the neutral activity line, with no build error and no
// log line.
func gooseSubagentRequestedTool(data json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ""
	}
	var call struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields[contracts.GooseSubagentRequestToolCall], &call) != nil {
		return ""
	}
	return call.Name
}

// gooseSubagentFromToolCallUpdate observes Goose's subagent tool requests.
// Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
// _meta payload: toolNotification.type is "message"; the discriminator is
// params.data.type == "subagent_tool_request". Each request carries the
// subagent_id and the tool_call name, so we upsert a running row with activity
// "tool: <name>" and persist the raw request to the child transcript. The
// spawn tool_call's final update closes the registry row.
//
// The registry row, the EnsureChildAgent linkage, and the closing update all
// key off the SPAWN toolCallId: the final spawn update carries only
// toolCallId, so the row must live under that key, and ChildAgentKey must
// match it or EnsureChildAgent would open a second row keyed by subagent_id
// that the close never reaches.
func gooseSubagentFromToolCallUpdate(tcu acpToolCallUpdateEnvelope) *acpSubagentObservation {
	// Final update on the spawn tool_call itself -> close the registry row.
	// Goose's final spawn update carries no _meta, but the row was created
	// (by the tool_call or a tool-request) under this toolCallId, so closing on
	// the final update is correct. CloseRow is idempotent: a plain tool with
	// no registry row is a no-op (the upsert path finds no row to close).
	if acpStatusIsFinal(tcu.Status) {
		return &acpSubagentObservation{
			RowKey:   tcu.ToolCallID,
			Status:   acpFinalStatus(tcu.Status),
			CloseRow: true,
			Mode:     acpModeCloseOnly,
		}
	}
	if len(tcu.Meta) == 0 {
		return nil
	}
	var meta struct {
		ToolNotification struct {
			Type   string          `json:"type"`
			Params json.RawMessage `json:"params"`
		} `json:"toolNotification"`
	}
	if err := json.Unmarshal(tcu.Meta, &meta); err != nil || meta.ToolNotification.Type != "message" {
		return nil
	}
	var params struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(meta.ToolNotification.Params, &params); err != nil {
		return nil
	}
	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(params.Data, &data); err != nil || data.Type != gooseSubagentToolRequestType {
		return nil
	}
	// Key the registry row, the child-agent linkage, AND the closing update off
	// the SPAWN toolCallId. The final spawn update knows only toolCallId, so
	// the row must live under that key; ChildAgentKey must match it too, or
	// EnsureChildAgent would upsert a SECOND row keyed by subagent_id that the
	// close never reaches (orphaned Running row). One Goose spawn = one child =
	// one transcript, so toolCallId is the correct stable child identity here;
	// the per-request subagent_id is not used as a registry key.
	childKey := tcu.ToolCallID
	activity := "tool request"
	if name := gooseSubagentRequestedTool(params.Data); name != "" {
		activity = "tool: " + name
	}
	// Persist the tool-request update to the child transcript (PersistChildMessage
	// via applySubagentObservation). Goose only ever ships requests, so this is
	// the live child activity. The payload is a tool_call_update-shaped envelope
	// carrying sessionUpdate + status + _meta so the shared ACP classifier
	// recognizes it and routes it to the subagent-tool-request renderer (a plain
	// re-marshal of the parsed struct drops sessionUpdate, leaving the classifier
	// no branch to match and the row renders as a raw-JSON dump).
	payload := gooseSubagentToolRequestPayload(tcu, meta.ToolNotification.Params)
	return &acpSubagentObservation{
		RowKey:                 tcu.ToolCallID,
		Title:                  "Goose subagent",
		Activity:               activity,
		Status:                 bgtask.StatusRunning,
		ChildAgentKey:          childKey,
		ChildTranscriptPayload: payload,
	}
}

// gooseSubagentToolRequestPayload builds the child-transcript payload for a
// Goose subagent tool-request update. It re-marshals the on-the-wire envelope
// (including sessionUpdate/status/_meta) rather than the parsed struct so the
// shared frontend ACP classifier recognizes the row as a tool_call_update and
// routes it to the subagent-tool-request renderer instead of falling through to
// the raw-JSON last resort.
func gooseSubagentToolRequestPayload(tcu acpToolCallUpdateEnvelope, notificationParams json.RawMessage) []byte {
	type toolRequestUpdate struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Status        string          `json:"status"`
		Kind          string          `json:"kind,omitempty"`
		Meta          json.RawMessage `json:"_meta,omitempty"`
	}
	// Re-wrap the original _meta (which carries toolNotification.params.data
	// at _meta.toolNotification) verbatim so the renderer can read the tool
	// name from params.data.tool_call.name.
	meta := tcu.Meta
	if len(meta) == 0 {
		// Fall back to a synthesized _meta if the envelope somehow lost it.
		meta = []byte(`{"toolNotification":{"type":"message","params":` + string(notificationParams) + `}}`)
	}
	payload, err := json.Marshal(toolRequestUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    tcu.ToolCallID,
		Status:        tcu.Status,
		Kind:          tcu.Title,
		Meta:          meta,
	})
	if err != nil {
		slog.Warn("goose subagent marshal transcript payload failed", "error", err)
		return []byte{}
	}
	return payload
}

func init() {
	// model + permissionMode (static group) + Goose's server-driven config options.
	registerPermissionModeConfigProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		StartGooseCLI,
		fallbackGooseCLIModes(),
		"LEAPMUX_GOOSE_DEFAULT_MODEL", "goose",
		GooseConfigThinkingEffort, GooseConfigProvider,
	)
	setPermissionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, PermissionDefaults{
		// Both halves are Smart Approve. The fallback must NOT be Goose's own `auto`,
		// which is the mode its bypass shortcut selects: a resumed session with no stored
		// mode would then open with every permission prompt disabled.
		NewSession: map[string]string{OptionIDPermissionMode: contracts.GooseDefaultMode},
		Fallback:   contracts.GooseDefaultMode,
	})
}
