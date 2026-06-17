package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/pathutil"
)

// Claude Code wire-format envelope types. Top-level `type` field on each
// NDJSON line emitted by the Claude Code SDK. Centralized here so the
// dispatch switches and downstream branches share a single source of
// truth and a typo turns into a compile error rather than a silent
// fall-through.
const (
	claudeMsgTypeAssistant            = "assistant"
	claudeMsgTypeUser                 = "user"
	claudeMsgTypeSystem               = "system"
	claudeMsgTypeResult               = "result"
	claudeMsgTypeControlRequest       = "control_request"
	claudeMsgTypeControlCancelRequest = "control_cancel_request"
	claudeMsgTypeControlResponse      = "control_response"
)

// claudeSystemSubtypeThinkingTokens is the `subtype` of the `system` telemetry
// line Claude Code emits during extended thinking. It carries a running
// `estimated_tokens` count for the in-flight turn and streams frequently. Like
// thinking-text deltas, it is live progress rather than timeline content, so we
// broadcast the latest estimate over the ephemeral agent_session_info channel
// and never persist it.
const claudeSystemSubtypeThinkingTokens = "thinking_tokens"

// SessionInfoKeyThinkingTokens is the agent_session_info wire key under which the
// running thinking-token estimate is broadcast. It happens to share the literal
// "thinking_tokens" with the Claude `subtype` above but is a distinct concept (a
// platform session-info key, not a Claude wire `subtype`); they are kept as
// separate consts so one can change without silently dragging the other.
// Exported so the service layer's dedup exemption keys off the exact same string
// the broadcast uses rather than a hand-copied literal that could drift.
const SessionInfoKeyThinkingTokens = "thinking_tokens"

// contextUsageSnapshot tracks token usage for debounced broadcasting.
type contextUsageSnapshot struct {
	mu                       sync.Mutex
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ContextWindow            int64
	// windowModel is the model id ContextWindow was derived for. The snapshot
	// outlives a model change (a live model switch, or the account-default sentinel
	// resolving to a concrete model after startup), so the window is re-seeded from
	// the catalog whenever the current model no longer matches this -- otherwise a
	// session that began on the 200K sentinel placeholder (or a smaller-window model)
	// would under-report a larger window until a result message happened to refresh it.
	windowModel   string
	LastBroadcast time.Time
}

// reseedWindow updates the snapshot's catalog window estimate when the model it was
// derived for no longer matches the current model: the snapshot outlives a model change
// (a live switch, or the account-default sentinel resolving to a concrete model after
// startup). It runs even when estimate is 0 (an unknown/unresolved model), so switching
// to such a model CLEARS a stale larger window carried over from the previous model --
// reverting to "unknown" rather than over-reporting -- and switching to a known model
// picks up its estimate immediately. A result message's window stays authoritative for
// its model because adoptResultWindow stamps windowModel too, so this estimate doesn't
// clobber it for the same model.
func (s *contextUsageSnapshot) reseedWindow(model string, estimate int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windowModel != model {
		s.ContextWindow = estimate
		s.windowModel = model
	}
}

// adoptResultWindow records the authoritative context window a result message reported
// for model, stamping windowModel so the catalog re-seed (reseedWindow) won't overwrite
// it for the same model. A non-positive window is ignored: top-level result messages
// always carry the primary model's window, but a subagent result that slipped past the
// parent_tool_use_id guard would not, and must not clear the real window.
func (s *contextUsageSnapshot) adoptResultWindow(model string, cw int64) {
	if cw <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ContextWindow = cw
	s.windowModel = model
}

// buildBroadcast assembles the context_usage broadcast payload from the current
// snapshot and reports whether it should be sent. It returns (nil, false) when no
// token usage has been recorded yet, or when the 10s debounce window has not elapsed
// for a non-result message; a result message always broadcasts. When it decides to
// broadcast it stamps LastBroadcast and includes context_window only when known
// (> 0), matching the "omit when unknown" contract reseedWindow/adoptResultWindow
// maintain. Takes s.mu, so the caller must not already hold it. now is passed in so
// the debounce is testable without a real clock.
func (s *contextUsageSnapshot) buildBroadcast(msgType string, now time.Time) (map[string]interface{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hasUsage := s.InputTokens > 0 || s.OutputTokens > 0 ||
		s.CacheCreationInputTokens > 0 || s.CacheReadInputTokens > 0
	if !hasUsage {
		return nil, false
	}
	shouldBroadcast := msgType == claudeMsgTypeResult ||
		now.Sub(s.LastBroadcast) >= 10*time.Second
	if !shouldBroadcast {
		return nil, false
	}
	s.LastBroadcast = now
	usageMap := map[string]interface{}{
		"input_tokens":                s.InputTokens,
		"output_tokens":               s.OutputTokens,
		"cache_creation_input_tokens": s.CacheCreationInputTokens,
		"cache_read_input_tokens":     s.CacheReadInputTokens,
	}
	if s.ContextWindow > 0 {
		usageMap["context_window"] = s.ContextWindow
	}
	return usageMap, true
}

// HandleOutput processes a single NDJSON line from Claude Code.
// This is the Claude Code-specific implementation of the Agent interface.
func (a *ClaudeCodeAgent) HandleOutput(content []byte) {
	a.handleClaudeOutput(content, "")
}

// handleClaudeOutput is the shared implementation. When msgType is empty, the
// type is parsed from the content; otherwise it uses the pre-parsed value from
// the output pipeline.
func (a *ClaudeCodeAgent) handleClaudeOutput(content []byte, msgType string) {
	if msgType == "" {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(content, &envelope); err != nil {
			slog.Warn("invalid agent output JSON", "agent_id", a.agentID, "error", err)
			return
		}
		msgType = envelope.Type
	}

	slog.Debug("HandleOutput", "agent_id", a.agentID, "type", msgType, "len", len(content))

	switch msgType {
	case claudeMsgTypeAssistant, claudeMsgTypeSystem, claudeMsgTypeResult:
		a.handlePersistableMessage(content, msgType)

	case claudeMsgTypeUser:
		if isSimpleUserTextEcho(content) {
			// Reset tool use counter at the start of each user turn.
			// Only reset for user text echoes, not tool_result messages,
			// so the counter accumulates across the entire turn.
			a.mu.Lock()
			a.turnToolUses = 0
			a.mu.Unlock()
		} else {
			a.handlePersistableMessage(content, msgType)
		}

	case NotificationTypeContextCleared, NotificationTypeInterrupted, NotificationTypePlanExecution:
		if msgType == NotificationTypeInterrupted {
			a.sink.ResetSpans()
		}
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content); err != nil {
			slog.Error("persist agent notification", "agent_id", a.agentID, "type", msgType, "error", err)
		}

	case claudeMsgTypeControlRequest:
		a.claudeCodeHandleControlRequest(content)

	case claudeMsgTypeControlCancelRequest:
		a.claudeCodeHandleControlCancel(content)

	case claudeMsgTypeControlResponse:
		a.claudeCodeHandleControlResponse(content)

	case NotificationTypeRateLimitEvent:
		a.claudeCodeHandleRateLimitEvent(content)

	default:
		a.sink.BroadcastStreamChunk(content, "", "")
	}
}

// enrichResultWithToolUses injects num_tool_uses into a result message so
// the frontend can determine whether the turn involved tool use.
func (a *ClaudeCodeAgent) enrichResultWithToolUses(content []byte) []byte {
	return a.enrichWithToolUses(content)
}

// contentBlock represents a single block in message.content[].
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
}

// messageEnvelope is the shared top-level structure parsed once for
// assistant, user, system, and result messages.
type messageEnvelope struct {
	ParentToolUseID string `json:"parent_tool_use_id"`
	ToolUseID       string `json:"tool_use_id"`
	Message         struct {
		RawContent json.RawMessage `json:"content"`
		Usage      *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ToolUseResult json.RawMessage            `json:"tool_use_result"`
	CostUSD       *float64                   `json:"total_cost_usd"`
	ModelUsage    map[string]json.RawMessage `json:"modelUsage"`
	IsError       bool                       `json:"is_error"`
	Result        string                     `json:"result"`

	// contentBlocks is lazily populated from RawContent.
	contentBlocks []contentBlock
	contentParsed bool
}

// ContentBlocks returns the parsed content blocks from message.content.
// Returns nil if content is not an array (e.g. a plain string).
func (e *messageEnvelope) ContentBlocks() []contentBlock {
	if !e.contentParsed {
		e.contentParsed = true
		raw := e.Message.RawContent
		if len(raw) > 0 && raw[0] == '[' {
			if err := json.Unmarshal(raw, &e.contentBlocks); err != nil {
				slog.Warn("claude content blocks unmarshal failed", "error", err)
			}
		}
	}
	return e.contentBlocks
}

// processAssistantBlocks iterates the pre-parsed message.content[] blocks of an
// assistant message and performs plan mode tracking, plan file tracking, tool
// use counting, and scope management.
func (a *ClaudeCodeAgent) processAssistantBlocks(env *messageEnvelope) {
	// Determine the parent span for any Agent tool_use blocks.
	parentSpanID := env.ParentToolUseID

	toolUseCount := 0
	planFileProcessed := false
	for _, block := range env.ContentBlocks() {
		if block.Type != "tool_use" {
			continue
		}

		toolUseCount++

		// Plan mode tracking (EnterPlanMode/ExitPlanMode).
		if block.ID != "" {
			switch block.Name {
			case ToolNameEnterPlanMode:
				a.sink.StorePlanModeToolUse(block.ID, PermissionModePlan)
			case ToolNameExitPlanMode:
				a.sink.StorePlanModeToolUse(block.ID, PermissionModeDefault)
			}

			a.sink.OpenSpan(block.ID, parentSpanID)
		}

		// Plan file path tracking (Write/Edit to ~/.claude/plans/).
		if !planFileProcessed && (block.Name == "Write" || block.Name == "Edit") {
			var input struct {
				FilePath  string `json:"file_path"`
				Content   string `json:"content"`
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			}
			if err := json.Unmarshal(block.Input, &input); err != nil {
				slog.Warn("claude tool input unmarshal failed", "agent_id", a.agentID, "tool", block.Name, "error", err)
				continue
			}
			filePath := input.FilePath
			if filePath != "" && a.homeDir != "" {
				planDir := filepath.Join(a.homeDir, ".claude", "plans")
				if pathutil.HasPathPrefix(filePath, planDir) {
					planFileProcessed = true

					var planContentStr string
					if block.Name == "Write" && input.Content != "" {
						planContentStr = input.Content
					} else {
						data, readErr := os.ReadFile(filePath)
						if readErr == nil && len(data) > 0 {
							if block.Name == "Edit" {
								planContentStr = strings.Replace(string(data), input.OldString, input.NewString, 1)
							} else {
								planContentStr = string(data)
							}
						}
					}

					var compressed []byte
					var compression leapmuxv1.ContentCompression
					if planContentStr != "" {
						compressed, compression = msgcodec.Compress([]byte(planContentStr))
					}
					a.sink.UpdatePlan(compressed, compression, extractPlanTitle(planContentStr))
				}
			}
		}
	}

	if toolUseCount > 0 {
		a.mu.Lock()
		a.turnToolUses += toolUseCount
		a.mu.Unlock()
	}
}

// handlePersistableMessage handles assistant, system, user, and result messages.
//
// Source for persistence is derived from msgType: USER for the `user`
// envelope (which on the Claude wire includes both human input and
// tool_result echoes under role:"user"); AGENT for assistant text,
// system notifications, and the terminal `result` envelope. `result`
// routes through PersistTurnEnd so its source value is unused.
func (a *ClaudeCodeAgent) handlePersistableMessage(content []byte, msgType string) {
	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	if msgType == claudeMsgTypeUser {
		source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	}

	if msgType == claudeMsgTypeSystem {
		// thinking_tokens is broadcast-only telemetry — intercept it before
		// session-init handling so its per-delta session_id doesn't needlessly
		// re-fire UpdateSessionID/BroadcastStatusActive, and before the persist
		// fallthrough so it never lands in the timeline.
		if a.handleThinkingTokens(content) {
			return
		}

		a.claudeCodeHandleSystemInit(content)

		if isNotificationThreadable(content, source) {
			if statusVal, ok := extractStatusValue(content); ok {
				prev := a.lastAgentStatus
				a.lastAgentStatus = statusVal
				if statusVal == "" && prev == "" {
					return
				}
			}
			// Persist the raw `system` message verbatim (source is AGENT
			// here — system notifications are agent-emitted). Includes
			// `compacting` status, api_retry, and other notification-
			// threaded subtypes — the renderer extracts `subtype`/`status`
			// from the raw envelope so future fields like `tokensBefore`/
			// `durationMs` don't get discarded.
			if _, err := a.sink.PersistNotification(source, content); err != nil {
				slog.Error("persist notification-threaded system message", "agent_id", a.agentID, "error", err)
			}
			return
		}
	}

	// Parse the message envelope once for all downstream consumers.
	var env messageEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		slog.Warn("invalid message envelope", "agent_id", a.agentID, "error", err)
		return
	}

	// Extract agent context metadata from top-level assistant and result
	// messages. Subagent messages (with parent_tool_use_id) have their own
	// smaller context and would make the bar show a misleadingly low value.
	if (msgType == claudeMsgTypeAssistant || msgType == claudeMsgTypeResult) && env.ParentToolUseID == "" {
		a.extractAndBroadcastUsage(&env, msgType)
	}

	// Determine parent span ID for hierarchy tracking.
	parentSpanID := env.ParentToolUseID
	if parentSpanID == "" {
		parentSpanID = env.ToolUseID
	}

	// Determine span ID and span type: for tool_use messages use the block ID
	// and tool name, for tool_result messages use the tool_use_id reference
	// and look up the tool name from the span tracker.
	var spanID, spanType string
	if msgType == claudeMsgTypeAssistant {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_use" && block.ID != "" {
				spanID, spanType = block.ID, block.Name
				break
			}
		}
	} else if msgType == claudeMsgTypeUser {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				spanID = block.ToolUseID
				break
			}
		}
		if spanID != "" {
			spanType = a.sink.GetSpanType(spanID)
		}
	}

	// Detect plan mode from tool_result messages.
	if msgType == claudeMsgTypeUser {
		a.detectPlanModeFromToolResult(&env)
	}

	// Enrich result messages with num_tool_uses.
	if msgType == claudeMsgTypeResult {
		content = a.enrichResultWithToolUses(content)
	}

	// Reserve the span color for tool_use messages (assistant with a spanID)
	// so it is available at persist time, before the span is actually opened.
	var spanColor int32
	if msgType == claudeMsgTypeAssistant && spanID != "" {
		spanColor = a.sink.ReserveSpanColor(spanID, parentSpanID)
	}

	// Persist as a standalone message with hierarchy metadata.
	// This MUST happen before processAssistantBlocks (which opens spans)
	// so the assistant message stays at the parent depth.
	// closing is true when this is a tool_result that will close its span.
	closing := msgType == claudeMsgTypeUser && spanID != ""
	spanInfo := SpanInfo{
		ParentSpanID: parentSpanID,
		SpanID:       spanID,
		SpanType:     spanType,
		SpanColor:    spanColor,
		Closing:      closing,
	}
	var persistErr error
	if msgType == claudeMsgTypeResult {
		// Terminal turn-end envelope — routes through PersistTurnEnd so
		// the sink fires the git-status auto-broadcast explicitly.
		persistErr = a.sink.PersistTurnEnd(content, spanInfo)
	} else {
		persistErr = a.sink.PersistMessage(source, content, spanInfo)
	}
	if persistErr != nil {
		slog.Error("persist agent message", "agent_id", a.agentID, "error", persistErr)
	}

	if spanType != "" {
		a.sink.SetSpanType(spanID, spanType)
	}

	// Parse assistant message content blocks for plan mode tracking,
	// plan file tracking, tool use counting, and span management.
	// Runs after persist so spans open AFTER the tool_use message,
	// keeping it at parent depth while its tool_result is indented.
	if msgType == claudeMsgTypeAssistant {
		a.processAssistantBlocks(&env)
	}

	// A single user message may contain multiple tool_result blocks
	// (parallel tool calls), so close all of them.
	if msgType == claudeMsgTypeUser {
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				a.sink.CloseSpan(block.ToolUseID)
			}
		}
	}

	if msgType == claudeMsgTypeResult {
		scheduleOrCancelAPIErrorAutoContinue(a.sink, env.IsError && isRetryableClaudeResultError(env.Result), content)

		// Reset all span tracking so the next turn starts clean.
		a.sink.ResetSpans()
	}
}

// handleThinkingTokens intercepts Claude Code's `system`/`thinking_tokens`
// telemetry. When the line is a thinking-token update it broadcasts the latest
// running estimate over the ephemeral agent_session_info channel (seq -1, never
// written to the messages table) and returns true so the caller skips both
// session-init handling and persistence. Returns false for any other system
// message, which continues down the normal persist path.
func (a *ClaudeCodeAgent) handleThinkingTokens(content []byte) bool {
	estimate, ok := parseThinkingTokens(content)
	if !ok {
		return false
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		SessionInfoKeyThinkingTokens: estimate,
	})
	return true
}

// parseThinkingTokens extracts the sanitized running thinking-token estimate
// from a Claude `system` line and reports whether the line is a thinking_tokens
// update at all. Kept pure (no sink, no I/O) so the sanitize rules below can be
// unit-tested directly rather than only through a full HandleOutput round-trip.
//
// estimated_tokens is captured as RawMessage, not a typed number, so the subtype
// match never depends on the count's wire form. A typed float64 (or int64) field
// would make json.Unmarshal error on a malformed or out-of-range count -- a
// quoted "230", an overflowing 1e400 -- returning ok=false and letting the
// telemetry fall through to session-init + persistence, i.e. the exact timeline
// bloat this interception exists to prevent. Matching the subtype first
// decouples "is this a thinking_tokens line?" from "did the count parse?".
//
// The count is parsed leniently as float64 so a fractional or exponent form
// (`230.0`, `1.5e4`) still reads, then sanitized to a non-negative int64 that is
// always a faithful, in-range count:
//   - a malformed, absent, or float64-overflowing count (1e400 -> +Inf ->
//     parse error, leaving the zero value) broadcasts 0;
//   - a negative count clamps to 0 (a running estimate is non-negative by
//     definition; a truncated -0.5 or a genuinely negative wire value both
//     become 0) so no consumer ever sees a negative count;
//   - a finite count at or above 2^63 (e.g. 1e300, which parses cleanly yet
//     saturates the int64 conversion to a garbage value) is out of range like
//     the overflowing 1e400, so it also broadcasts 0 rather than a nonsense
//     9.2-quintillion count.
func parseThinkingTokens(content []byte) (estimate int64, ok bool) {
	var msg struct {
		Subtype         string          `json:"subtype"`
		EstimatedTokens json.RawMessage `json:"estimated_tokens"`
	}
	if err := json.Unmarshal(content, &msg); err != nil || msg.Subtype != claudeSystemSubtypeThinkingTokens {
		return 0, false
	}
	var f float64
	if len(msg.EstimatedTokens) > 0 {
		_ = json.Unmarshal(msg.EstimatedTokens, &f)
	}
	if f < 0 || f >= float64(math.MaxInt64) {
		f = 0
	}
	return int64(f), true
}

// claudeCodeHandleSystemInit extracts session_id from system init messages.
func (a *ClaudeCodeAgent) claudeCodeHandleSystemInit(content []byte) {
	var initMsg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(content, &initMsg); err != nil || initMsg.SessionID == "" {
		return
	}
	a.sink.UpdateSessionID(initMsg.SessionID)
	a.sink.BroadcastStatusActive(initMsg.SessionID)
}

// claudeCodeHandleControlRequest persists and broadcasts a control_request.
func (a *ClaudeCodeAgent) claudeCodeHandleControlRequest(content []byte) {
	var cr struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cr); err != nil {
		slog.Warn("invalid control_request JSON", "agent_id", a.agentID, "error", err)
		return
	}
	a.sink.PersistControlRequest(cr.RequestID, content)
	a.sink.BroadcastControlRequest(cr.RequestID, content)
}

// claudeCodeHandleControlCancel persists and broadcasts a control_cancel_request.
func (a *ClaudeCodeAgent) claudeCodeHandleControlCancel(content []byte) {
	var cc struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cc); err != nil {
		slog.Warn("invalid control_cancel_request JSON", "agent_id", a.agentID, "error", err)
		return
	}
	a.sink.DeleteControlRequest(cc.RequestID)
	a.sink.BroadcastControlCancel(cc.RequestID)
}

// claudeCodeHandleControlResponse handles a control_response from Claude Code that no
// pending waiter consumed -- in practice a DEFERRED set_permission_mode ack. The live
// UpdateSettings path caps its wait at permissionModeApplyTimeout; while a turn is streaming
// the CLI defers the ack until the turn ends, so UpdateSettings applied the mode
// optimistically and this late response is the authoritative reconciliation.
//
// Re-sync from the provider rather than trusting the optimistic value: adopt the mode the CLI
// actually applied (response.mode -- present on a success, and on a rejection that reports the
// still-effective mode) into BOTH the in-memory confirmed state AND the persisted row +
// broadcast. Updating a.confirmedPermissionMode is the part the optimistic path can't do
// itself: OptionGroups() reads confirmedPermissionMode, so without this the agent's catalog
// would keep reporting the optimistic mode even after the CLI settled on a different one.
// (get_settings omits permission mode and, run from this reader goroutine, would deadlock on
// its own response -- so the response.mode field is the only provider-authoritative source.)
func (a *ClaudeCodeAgent) claudeCodeHandleControlResponse(content []byte) {
	var cr struct {
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Mode string `json:"mode"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &cr); err != nil {
		return
	}
	mode := cr.Response.Response.Mode
	if mode == "" {
		return
	}
	// Fold the mode back ONLY when this response is the deferred ack of the set_permission_mode
	// toggle we are awaiting -- matched by request_id. Without the match a stale/duplicate ack,
	// an earlier (superseded) toggle's ack, or any other mode-bearing control_response would
	// clobber the confirmed mode with a value the user didn't last choose. Consuming the id
	// (clearing it) also makes a re-delivered ack a no-op.
	a.mu.Lock()
	matched := cr.Response.RequestID != "" && cr.Response.RequestID == a.deferredPermissionModeReqID
	if matched {
		a.deferredPermissionModeReqID = ""
		a.confirmedPermissionMode = mode
		// The deferred ack confirming "auto" proves the session can enter it; clear a stale
		// autoModeAvailable=false (see applyPermissionModeLive) so the picker offers auto again.
		if mode == PermissionModeAuto {
			a.autoModeAvailable = true
		}
	}
	a.mu.Unlock()
	if matched {
		a.sink.UpdatePermissionMode(mode)
	}

	// No need to persist control_response in the timeline — they are
	// already surfaced as notification threads.
}

// claudeCodeHandleRateLimitEvent broadcasts rate_limit_event and persists as notification.
func (a *ClaudeCodeAgent) claudeCodeHandleRateLimitEvent(content []byte) {
	var rle struct {
		RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(content, &rle); err != nil || len(rle.RateLimitInfo) == 0 {
		return
	}

	// Decode the SDK's camelCase shape and re-emit as snake_case to match
	// the platform's session-info wire format. New fields the SDK adds in
	// the future need to be enumerated here explicitly so they pick up the
	// correct casing on the wire — the persisted notification still
	// carries the raw Claude shape, so consumers that need an unknown
	// field can fall back to that path.
	var rlInfo struct {
		RateLimitType      string   `json:"rateLimitType"`
		Status             string   `json:"status"`
		ResetsAt           *int64   `json:"resetsAt"`
		Utilization        *float64 `json:"utilization,omitempty"`
		SurpassedThreshold *float64 `json:"surpassedThreshold,omitempty"`
		OverageStatus      string   `json:"overageStatus,omitempty"`
		OverageResetsAt    *int64   `json:"overageResetsAt,omitempty"`
		IsUsingOverage     *bool    `json:"isUsingOverage,omitempty"`
	}
	if err := json.Unmarshal(rle.RateLimitInfo, &rlInfo); err != nil {
		slog.Warn("claude rate limit info unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	if rlInfo.RateLimitType == "" {
		rlInfo.RateLimitType = "unknown"
	}

	tier := map[string]any{
		"rate_limit_type": rlInfo.RateLimitType,
		"status":          rlInfo.Status,
	}
	if rlInfo.Utilization != nil {
		tier["utilization"] = *rlInfo.Utilization
	}
	if rlInfo.ResetsAt != nil {
		tier["resets_at"] = *rlInfo.ResetsAt
	}
	if rlInfo.SurpassedThreshold != nil {
		tier["surpassed_threshold"] = *rlInfo.SurpassedThreshold
	}
	if rlInfo.OverageStatus != "" {
		tier["overage_status"] = rlInfo.OverageStatus
	}
	if rlInfo.OverageResetsAt != nil {
		tier["overage_resets_at"] = *rlInfo.OverageResetsAt
	}
	if rlInfo.IsUsingOverage != nil {
		tier["is_using_overage"] = *rlInfo.IsUsingOverage
	}

	a.sink.BroadcastSessionInfo(map[string]interface{}{
		"rate_limits": map[string]any{rlInfo.RateLimitType: tier},
	})

	// Persist the raw `rate_limit_event` envelope verbatim as an
	// agent-emitted notification. The frontend's extractRateLimitInfo /
	// notification renderer reads `rate_limit_info` from this raw
	// Claude-native shape (camelCase) — the persisted side stays in
	// the SDK's format so notification rendering remains a passthrough.
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("persist rate_limit notification", "agent_id", a.agentID, "error", err)
	}

	if rlInfo.Status == "allowed" {
		a.sink.CancelAutoContinue(AutoContinueReasonRateLimit)
		return
	}
	if rlInfo.ResetsAt == nil {
		return
	}

	a.sink.ScheduleAutoContinue(AutoContinueSchedule{
		Reason:        AutoContinueReasonRateLimit,
		DueAt:         time.Unix(*rlInfo.ResetsAt, 0).UTC(),
		SourcePayload: append([]byte(nil), rle.RateLimitInfo...),
	})
}

// extractAndBroadcastUsage extracts token usage from assistant/result messages.
func (a *ClaudeCodeAgent) extractAndBroadcastUsage(env *messageEnvelope, msgType string) {
	info := map[string]interface{}{}
	if env.CostUSD != nil {
		info["total_cost_usd"] = *env.CostUSD
	}

	// Snapshot a.model and the effort resolver under a.mu in one acquisition: this
	// runs on the readOutputLoop goroutine while refreshSettingsFromAgent may
	// concurrently rewrite a.model under the same lock, so the a.mu pairing is what the
	// a.model read needs. a.availableModels (which the resolver captures) is written
	// only during the pre-registration startup handshake (convertClaudeModels, then a
	// possible ensureSettledModelListed insert) and never mutated afterward. This read
	// happens-after that window: it fires only for assistant/result output, which needs
	// a user turn, and a user turn can't reach this agent until it has been registered
	// with the manager -- the registration's lock provides the happens-before edge that
	// publishes every startup write. So the resolver needs a.mu only for the a.model
	// read it pairs with here; the catalog field is already safely published. The
	// catalog entries are immutable shared data, so the window lookup is safe to compute
	// after unlocking.
	a.mu.Lock()
	model := a.model
	resolver := a.effortResolver()
	a.mu.Unlock()
	// The catalog window is an ESTIMATE inferred from the model id ("[1m]" => 1M, else
	// 200K). resolver.contextWindow resolves it over the dynamic catalog with the static
	// fallback -- the same dynamic-first-then-fallback the effort lookups use -- so a
	// model the live CLI dropped from its list but a resumed session is still running
	// keeps its known window instead of going dark. It is 0 only when the model has no
	// known window in EITHER catalog: the unresolved account-default sentinel (its entry
	// is a placeholder), or a model absent from both lists. We deliberately do NOT
	// fabricate a window then -- 0 means "unknown" and the broadcast omits context_window
	// below, matching the frontend, which likewise shows no window when it can't resolve
	// one. For a concrete model absent from both catalogs, a result message's modelUsage
	// supplies the real window once one arrives (findPrimaryContextWindow matches its
	// concrete key). The unresolved sentinel ("default") can't: it matches no concrete
	// usage key, so it stays unknown until the model resolves off the sentinel (a later
	// refreshSettingsFromAgent), whose model change re-seeds the window here.
	contextWindow := resolver.contextWindow(model)

	snapshot := a.getOrCreateUsageSnapshot()
	snapshot.reseedWindow(model, contextWindow)

	if msgType == claudeMsgTypeAssistant && env.Message.Usage != nil {
		u := env.Message.Usage
		snapshot.mu.Lock()
		snapshot.InputTokens = u.InputTokens
		snapshot.OutputTokens = u.OutputTokens
		snapshot.CacheCreationInputTokens = u.CacheCreationInputTokens
		snapshot.CacheReadInputTokens = u.CacheReadInputTokens
		snapshot.mu.Unlock()
	}

	if msgType == claudeMsgTypeResult && env.ModelUsage != nil {
		// Find the context window for the primary model in the usage map. Top-level
		// result messages include cumulative session-level usage that always contains
		// the primary model's entry. Subagent results (if they bypass the outer
		// parent_tool_use_id guard) only contain the subagent's model and will not
		// match the primary; findPrimaryContextWindow returns 0 for that, and
		// adoptResultWindow ignores it rather than overwriting with a smaller window.
		snapshot.adoptResultWindow(model, findPrimaryContextWindow(env.ModelUsage, model))
	}

	if usageMap, ok := snapshot.buildBroadcast(msgType, time.Now()); ok {
		info["context_usage"] = usageMap
	}

	if len(info) > 0 {
		a.sink.BroadcastSessionInfo(info)
	}
}

// getOrCreateUsageSnapshot returns the usage snapshot, creating an empty one on
// first use. The window is NOT seeded here: every caller calls reseedWindow
// immediately afterward, which is the single source of the estimated window (it
// also stamps windowModel, which a constructor seed cannot). a.contextUsage is only
// ever touched from the readOutputLoop goroutine, so it needs no lock of its own;
// the snapshot's own fields are guarded by snapshot.mu.
func (a *ClaudeCodeAgent) getOrCreateUsageSnapshot() *contextUsageSnapshot {
	if a.contextUsage == nil {
		a.contextUsage = &contextUsageSnapshot{}
	}
	return a.contextUsage
}

// modelContextWindow looks up the context window for a model ID from a list
// of available models. Returns 0 if the model is not found. Delegates to
// FindAvailableModel so the nil-entry guard and id match live in one place
// rather than a fourth hand-copied catalog walk.
func modelContextWindow(models []*ModelInfo, modelID string) int64 {
	if m := FindAvailableModel(models, modelID); m != nil {
		return m.ContextWindow
	}
	return 0
}

// findPrimaryContextWindow extracts the context window for the primary model from a
// modelUsage map. The modelUsage keys are full API model IDs (e.g.
// "claude-opus-4-6[1m]") while shortModelID is the short alias (e.g. "opus[1m]").
// Each key is collapsed into the alias space with normalizeClaudeCodeModel -- the
// same normalization a.model and the catalog ids use -- and compared for EQUALITY,
// so the match is exact rather than a substring scan: "opus" no longer matches an
// unrelated "claude-opusplus-1" key, and a "[1M]" spelling is handled (normalize
// lowercases).
//
// Because Opus collapses to a single "opus[1m]" alias regardless of suffix, two keys
// (a standard-context "claude-opus-4-6" and a 1M "claude-opus-4-6[1m]") could both
// match -- a case the current CLI does not emit (it lists only the 1M Opus), but one
// the old per-suffix disambiguation handled. Return the LARGEST matching window rather
// than the first hit so the result is deterministic regardless of map iteration order
// (the 1M window is the correct one for the running Opus). Returns 0 if the primary
// model is not found.
func findPrimaryContextWindow(modelUsage map[string]json.RawMessage, shortModelID string) int64 {
	if shortModelID == "" {
		// No primary model configured -- fall back to max across all models.
		return maxContextWindow(modelUsage)
	}
	want := normalizeClaudeCodeModel(shortModelID)
	var best int64
	for key, raw := range modelUsage {
		if normalizeClaudeCodeModel(key) != want {
			continue
		}
		if cw := contextWindowOf(raw); cw > best {
			best = cw
		}
	}
	return best
}

// contextWindowOf unmarshals a single modelUsage entry and returns its
// contextWindow, or 0 when the entry is malformed or carries no positive window.
func contextWindowOf(raw json.RawMessage) int64 {
	var mu struct {
		ContextWindow int64 `json:"contextWindow"`
	}
	if json.Unmarshal(raw, &mu) == nil {
		return mu.ContextWindow
	}
	return 0
}

// maxContextWindow returns the largest contextWindow across all models.
func maxContextWindow(modelUsage map[string]json.RawMessage) int64 {
	var maxCW int64
	for _, raw := range modelUsage {
		if cw := contextWindowOf(raw); cw > maxCW {
			maxCW = cw
		}
	}
	return maxCW
}

// detectPlanModeFromToolResult inspects a user message (tool_result) for
// confirmation of a previously tracked EnterPlanMode or ExitPlanMode tool_use.
func (a *ClaudeCodeAgent) detectPlanModeFromToolResult(env *messageEnvelope) {
	for _, block := range env.ContentBlocks() {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}

		targetMode, ok := a.sink.LoadAndDeletePlanModeToolUse(block.ToolUseID)
		if !ok {
			continue
		}

		resultText := ""
		if len(env.ToolUseResult) > 0 {
			resultText = extractToolUseResultMessage(env.ToolUseResult)
		}

		resultLower := strings.ToLower(resultText)
		confirmed := false
		if targetMode == PermissionModePlan && strings.Contains(resultLower, "entered plan mode") {
			confirmed = true
		} else if targetMode == PermissionModeDefault && strings.Contains(resultLower, "approved your plan") {
			confirmed = true
		}

		if confirmed {
			slog.Info("plan mode change confirmed via tool_result",
				"agent_id", a.agentID,
				"tool_use_id", block.ToolUseID,
				"mode", targetMode)
			a.sink.UpdatePermissionMode(targetMode)
		} else {
			truncated := resultText
			if len(truncated) > 64 {
				truncated = truncated[:64]
			}
			slog.Debug("plan mode tool_result did not contain expected confirmation",
				"agent_id", a.agentID,
				"tool_use_id", block.ToolUseID,
				"expected_mode", targetMode,
				"result_text", truncated)
		}
	}
}

// extractToolUseResultMessage extracts the message string from a tool_use_result
// field that may be either a plain JSON string or an object with a "message" key.
func extractToolUseResultMessage(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case '{':
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &obj) == nil {
			return obj.Message
		}
	}
	return ""
}

// --- Notification threading helpers ---

// isNotificationThreadable decides whether a notification envelope can
// participate in thread consolidation. Only invoked on the
// PersistNotification path, so the AGENT branch always means
// "agent-emitted system notification" — never assistant text or tool
// content (those go through PersistMessage / PersistTurnEnd).
func isNotificationThreadable(content []byte, source leapmuxv1.MessageSource) bool {
	switch source {
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX:
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(content, &msg); err != nil {
			slog.Warn("notification threadable unmarshal failed", "source", "leapmux", "error", err)
			return false
		}
		switch msg.Type {
		case NotificationTypeSettingsChanged,
			NotificationTypeContextCleared,
			NotificationTypeInterrupted,
			NotificationTypeRateLimit,
			NotificationTypeAgentError,
			NotificationTypeCompacting:
			return true
		}
		return false
	case leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT:
		return ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE).Classify(content).Consolidatable()
	default:
		return false
	}
}

func extractStatusValue(content []byte) (status string, ok bool) {
	var msg struct {
		Subtype string  `json:"subtype"`
		Status  *string `json:"status"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		slog.Warn("extract status value unmarshal failed", "error", err)
		return "", false
	}
	if msg.Subtype != "status" {
		return "", false
	}
	if msg.Status != nil {
		return *msg.Status, true
	}
	return "", true
}

var claudeSyntheticAPI5xxPattern = regexp.MustCompile(`^API Error[^[:alnum:]]+5[0-9]{2}(?:$|[^[:alnum:]].*)`)
var claudeRetryableIdleTimeoutPattern = regexp.MustCompile(`^API Error[^[:alnum:]]+Stream idle timeout(?:$|[^[:alnum:]].*)`)

// isRetryableClaudeResultError reports whether a Claude result error should
// trigger auto-continue.
func isRetryableClaudeResultError(s string) bool {
	return claudeSyntheticAPI5xxPattern.MatchString(s) || claudeRetryableIdleTimeoutPattern.MatchString(s)
}

// isSimpleUserTextEcho returns true if the NDJSON line is a user message echo
// with string content (not a tool_result array).
func isSimpleUserTextEcho(content []byte) bool {
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		slog.Warn("user text echo unmarshal failed", "error", err)
		return false
	}
	if msg.Type != claudeMsgTypeUser {
		return false
	}
	trimmed := bytes.TrimSpace(msg.Message.Content)
	return len(trimmed) > 0 && trimmed[0] == '"'
}
