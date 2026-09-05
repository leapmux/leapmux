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

	"github.com/leapmux/leapmux/generated/contracts"
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
	claudeMsgTypeToolProgress         = "tool_progress"
)

// claudeSystemSubtypeThinkingTokens is the `subtype` of the `system` telemetry
// line Claude Code emits during extended thinking. It carries a running
// `estimated_tokens` count for the in-flight turn and streams frequently. Like
// thinking-text deltas, it is live progress rather than timeline content, so we
// broadcast the latest estimate over the ephemeral agent_session_info channel
// and never persist it.
//
// It happens to share the literal "thinking_tokens" with
// contracts.SessionInfoKeyThinkingTokens, the agent_session_info key the
// estimate is broadcast under, but the two are distinct concepts -- a Claude
// wire `subtype` against a platform session-info key -- so they stay separate
// and a change to one does not force a change to the other.
const claudeSystemSubtypeThinkingTokens = "thinking_tokens"

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
	usageMap := contextUsageMap(contextTokenCounts{
		Input:      s.InputTokens,
		Output:     s.OutputTokens,
		CacheWrite: s.CacheCreationInputTokens,
		CacheRead:  s.CacheReadInputTokens,
	})
	if s.ContextWindow > 0 {
		usageMap[contracts.ContextUsageFieldContextWindow] = s.ContextWindow
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

	case claudeMsgTypeToolProgress:
		a.claudeHandleToolProgress(content)

	default:
		// A type this switch does not know is DROPPED, never forwarded. The
		// switch used to end in a catch-all that called
		// `BroadcastStreamChunk(content, "", "")`, and a span-less chunk is
		// appended verbatim to the chat's free-form streaming text -- so every
		// unknown CLI frame printed into the transcript as raw JSON, which is
		// what tool_progress did before the case above claimed it.
		//
		// `stream_event` is not an exception to this. StartClaudeCode passes no
		// `--include-partial-messages`, and the CLI emits the type only with that
		// flag, so nothing arrives today; and if the flag is ever added, the
		// frames carry incremental deltas that need real extraction. A verbatim
		// forward would print the envelopes instead. Whoever adds the flag adds
		// the extraction with it.
		slog.Debug("unhandled claude output type", "agent_id", a.agentID, "type", msgType)
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
	ToolUseResult json.RawMessage `json:"tool_use_result"`
	// CostUSD reads Claude Code's OWN name for the turn cost. This tag must stay
	// equal to contracts.SessionInfoKeyTotalCostUsd: the worker persists this
	// result line unchanged, and the browser reads the persisted row through that
	// constant. A struct tag takes a literal only, so it cannot follow a rename --
	// checkSessionInfo pins the token in the generator instead.
	CostUSD    *float64                   `json:"total_cost_usd"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	IsError    bool                       `json:"is_error"`
	Result     string                     `json:"result"`

	// contentBlocks is lazily populated from RawContent.
	contentBlocks []contentBlock
	contentParsed bool
}

// claudeUserEnvelopeMarkType classifies a persisted Claude `user`-envelope row for
// the scroll rail's jump marks. The only Claude user row that carries a mark at
// ingestion is a self-displaying control answer -- an AskUserQuestion / ExitPlanMode
// tool_result Claude re-emits into its own transcript -- which is marked
// CONTROL_RESPONSE. Everything else is unmarked. spanType is the resolved tool name
// for tool_result rows (empty otherwise).
//
// HandleOutput drops string-content user envelopes before persistence. Queue
// acceptance writes the USER_MESSAGE mark on the persisted input row. This
// classifier leaves other user text unmarked to prevent a duplicate jump target.
func claudeUserEnvelopeMarkType(spanType string) leapmuxv1.MarkType {
	if (claudeProvider{}).IsSelfDisplayingControlTool(spanType) {
		return leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE
	}
	return leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED
}

func claudeUserEnvelopeBlocksMarkType(blocks []contentBlock, spanTypeFor func(string) string) leapmuxv1.MarkType {
	for _, block := range blocks {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		if claudeUserEnvelopeMarkType(spanTypeFor(block.ToolUseID)) == leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE {
			return leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE
		}
	}
	return leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED
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

	// A SendMessage here may be about to restart a finished subagent. Record the
	// recipient before the tool runs, because the task_started it produces cannot
	// be told from a session resume's hydration burst on its own. This is the
	// ROOT transcript, so the arms are scoped to it ("").
	a.claudeArmRevivesFromBlocks(env, "")

	toolUseCount := 0
	planFileProcessed := false
	for _, block := range env.ContentBlocks() {
		if block.Type != "tool_use" {
			continue
		}

		toolUseCount++

		// Plan mode tracking (EnterPlanMode/ExitPlanMode).
		if block.ID != "" {
			a.sink.SetSpanType(block.ID, block.Name)

			switch block.Name {
			case ToolNameEnterPlanMode:
				a.sink.StorePlanModeToolUse(block.ID, contracts.ClaudeModePlan)
			case ToolNameExitPlanMode:
				a.sink.StorePlanModeToolUse(block.ID, contracts.ClaudeModeDefault)
			}

			// A subagent spawn opens no span (claudeToolSpawnsSubagent). Its
			// span type is still recorded above, because the tool_result path
			// reads it back through GetSpanType to persist span_type.
			if !claudeToolSpawnsSubagent(block.Name) {
				a.sink.OpenSpan(block.ID, parentSpanID)
			}
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

// claudeSpanForEnvelope resolves which span a Claude envelope belongs to.
//
// An assistant envelope takes the FIRST tool_use block's id and tool name; a
// user envelope takes the first tool_result's tool_use_id and looks its type up
// through spanTypeFor, which is the tracker of whichever transcript the row
// lands in. Every other envelope belongs to no span.
func claudeSpanForEnvelope(msgType string, env *messageEnvelope, spanTypeFor func(string) string) (spanID, spanType string, closing bool) {
	switch msgType {
	case claudeMsgTypeAssistant:
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_use" && block.ID != "" {
				return block.ID, block.Name, false
			}
		}
	case claudeMsgTypeUser:
		for _, block := range env.ContentBlocks() {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				return block.ToolUseID, spanTypeFor(block.ToolUseID), true
			}
		}
	}
	return "", "", false
}

// claudeSpanInfoFor builds the SpanInfo a Claude envelope persists with, and
// reserves the tool_use row's color against sink.
//
// Reserving (not opening) is what makes the color available at persist time
// while the span is still closed, so the tool_use row can carry the color its
// rail will use without yet drawing that rail.
//
// A subagent spawn reserves nothing: it opens no span, so it draws no rail and
// its card takes the neutral border. Reserving anyway would also block that
// color from the next real span until the spawn's tool_result landed. NoSpan
// then tells the persist path that the resulting color 0 is the answer.
//
// Shared by the parent transcript (handlePersistableMessage) and the child one
// (routeSubagentMessage), which differ only in the sink and the parent span id.
// They held two copies of this rule, so the spawn guard had to be written twice.
func claudeSpanInfoFor(sink OutputSink, msgType string, env *messageEnvelope, parentSpanID string) SpanInfo {
	spanID, spanType, closing := claudeSpanForEnvelope(msgType, env, sink.GetSpanType)
	spawns := spanID != "" && claudeToolSpawnsSubagent(spanType)

	var spanColor int32
	if msgType == claudeMsgTypeAssistant && spanID != "" && !spawns {
		spanColor = sink.ReserveSpanColor(spanID, parentSpanID)
	}
	// A self-displaying control tool (AskUserQuestion/ExitPlanMode) forwarded as
	// a tool_result gets its CONTROL_RESPONSE scroll-rail mark.
	var markType leapmuxv1.MarkType
	if msgType == claudeMsgTypeUser {
		markType = claudeUserEnvelopeBlocksMarkType(env.ContentBlocks(), sink.GetSpanType)
	}
	return SpanInfo{
		ParentSpanID: parentSpanID,
		SpanID:       spanID,
		SpanType:     spanType,
		SpanColor:    spanColor,
		Closing:      closing,
		MarkType:     markType,
		NoSpan:       spawns,
	}
}

// claudeCloseToolResultSpans closes the span of EVERY tool_result in a user
// envelope. One user message can carry parallel tool calls, so closing only the
// first would leak the rest until the turn's bulk ResetSpans.
func claudeCloseToolResultSpans(sink OutputSink, env *messageEnvelope) {
	for _, block := range env.ContentBlocks() {
		if block.Type == "tool_result" && block.ToolUseID != "" {
			sink.CloseSpan(block.ToolUseID)
		}
	}
}

// handlePersistableMessage handles assistant, system, user, and result messages.
//
// Source for persistence is derived from msgType: USER for the `user`
// envelope (which on the Claude wire includes both human input and
// tool_result echoes under role:"user"); AGENT for assistant text,
// system notifications, and the final `result` envelope. `result`
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

		// Task/Workflow events drive the background-task registry (and, for
		// Task subagents, the child transcript via --forward-subagent-text).
		// They are consumed here and never persist into the parent transcript.
		if a.claudeHandleTaskEvent(content) {
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

	// Route subagent output (forwarded via --forward-subagent-text) into the
	// child's OWN transcript. Every forwarded envelope -- assistant text,
	// thinking, the child's own tool_use, its tool_result, and the child's
	// result -- carries the SAME parent_tool_use_id (the parent's Task
	// tool_use). None of it persists into the parent transcript.
	if env.ParentToolUseID != "" && (msgType == claudeMsgTypeAssistant ||
		msgType == claudeMsgTypeUser || msgType == claudeMsgTypeResult) {
		a.routeSubagentMessage(content, msgType, &env)
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

	// Detect plan mode from tool_result messages.
	if msgType == claudeMsgTypeUser {
		a.detectPlanModeFromToolResult(&env)
	}

	// Enrich result messages with num_tool_uses.
	if msgType == claudeMsgTypeResult {
		content = a.enrichResultWithToolUses(content)
	}

	// Resolve the span metadata and reserve the tool_use row's color. Shared
	// with the child-transcript path in routeSubagentMessage.
	spanInfo := claudeSpanInfoFor(a.sink, msgType, &env, parentSpanID)
	spanID, spanType := spanInfo.SpanID, spanInfo.SpanType

	// Persist as a standalone message with hierarchy metadata.
	// This MUST happen before processAssistantBlocks (which opens spans)
	// so the assistant message stays at the parent depth.
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

	if msgType == claudeMsgTypeUser {
		claudeCloseToolResultSpans(a.sink, &env)
	}

	if msgType == claudeMsgTypeResult {
		a.mu.Lock()
		a.turnActive = false
		a.mu.Unlock()
		scheduleOrCancelAPIErrorAutoContinue(a.sink, env.IsError && isRetryableClaudeResultError(env.Result), content)

		// Reset all span tracking so the next turn starts clean.
		a.sink.ResetSpans()
		// Drop the ROOT's SendMessage revive arms with it. A revive's task_started
		// lands inside the turn that sent the message (the tool awaits the
		// restart), so an arm still standing here addressed a live subagent, a
		// recipient outside this session, or a send the CLI refused -- and none of
		// those will ever fire it.
		//
		// Only the root's. A subagent runs past this boundary and clears its own
		// arms at its own turn end, so wiping every arm here would drop a live
		// subagent's before its task_started arrived.
		a.tasks.clearClaudeRevives("")
		notifyInputReady(a.sink)
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
		contracts.SessionInfoKeyThinkingTokens: estimate,
	})
	return true
}

// claudeHandleToolProgress intercepts Claude Code's `tool_progress` frames and
// broadcasts what a running tool's card can show over the ephemeral
// agent_session_info channel (seq -1, never written to the messages table).
//
// The frame MUST be claimed here, in the type switch, before any envelope
// parsing: it carries a parent_tool_use_id, and that field is what routes a
// message into a subagent's child transcript (handlePersistableMessage). It
// also used to fall through to the default case, which broadcast the raw line
// as a span-less stream chunk -- and that printed the JSON into the chat tail
// whenever no span was open, which is exactly the state during a top-level
// Agent/Task call (claudeToolSpawnsSubagent opens none).
func (a *ClaudeCodeAgent) claudeHandleToolProgress(content []byte) {
	update, ok := parseClaudeToolProgress(content)
	if !ok {
		return
	}
	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool: update,
	})
}

// claudeToolProgress is the subset of a `tool_progress` frame this worker reads.
// Verified against claude 2.1.258, both in the CLI's own output schema and in a
// live capture.
type claudeToolProgress struct {
	// ParentToolUseID is the tool_use id of the tool the frame describes. The
	// frame's OWN tool_use_id is synthetic in every family that reaches us --
	// `<realId>-heartbeat-<n>` for a heartbeat, `agent_<messageId>` for a
	// subagent retry -- so it identifies nothing the transcript holds and is
	// never read here.
	ParentToolUseID string `json:"parent_tool_use_id"`
	// ElapsedSeconds is a RawMessage for the reason parseThinkingTokens gives
	// for estimated_tokens: a typed field would make the whole frame fail to
	// decode on a malformed count, and the badge would lose a heartbeat it could
	// otherwise read.
	ElapsedSeconds json.RawMessage `json:"elapsed_time_seconds"`
	Heartbeat      bool            `json:"heartbeat"`
	// SubagentType discriminates the agent_api_retry family. It is read for that
	// alone and never broadcast: the tool card takes the subagent's name from the
	// tool_use row's own input.
	SubagentType  string               `json:"subagent_type"`
	SubagentRetry *claudeSubagentRetry `json:"subagent_retry"`
	// The frame also carries `tool_name`. No field captures it, on purpose: the
	// card already shows the tool's name from the tool_use row, so nothing reads
	// it here -- and an absent field cannot fail the decode, which a mistyped
	// `tool_name` would otherwise do, costing the badge a readable heartbeat.
}

// claudeSubagentRetry is the retry state an `agent_api_retry` frame carries
// while a Task subagent retries an API call.
type claudeSubagentRetry struct {
	Attempt       int    `json:"attempt"`
	MaxRetries    int    `json:"max_retries"`
	RetryDelayMs  int64  `json:"retry_delay_ms"`
	ErrorStatus   *int   `json:"error_status"`
	ErrorCategory string `json:"error_category"`
}

// parseClaudeToolProgress turns a `tool_progress` frame into the running_tool
// update to broadcast, or reports ok=false for a frame with nothing to show.
// Pure (no sink, no I/O) so the family rules are unit-testable directly.
//
// The CLI emits five families under this one type. Two reach LeapMux:
//
//   - tool_heartbeat -- every 30 seconds, for every tool call of the MAIN agent
//     (a tool running inside a subagent gets none). It carries the elapsed
//     time, and no frame marks the end: the CLI clears its timer in the tool
//     call's `finally`. The frontend drops the entry when the tool_result row
//     lands, so no "ended" message is needed here.
//   - agent_api_retry -- a Task call whose subagent hit an API error. Its
//     elapsed_time_seconds is always 0, so it must NOT touch the elapsed value
//     the heartbeats maintain; the update omits the key entirely. When the
//     retry succeeds the CLI repeats the frame with subagent_retry ABSENT, and
//     that is the only resolved signal, so an explicit nil retry is sent to
//     clear the badge.
//
// The other three are unreachable: bash_progress and powershell_progress need
// CLAUDE_CODE_REMOTE or a container id, and repl_tool_call needs the REPL tool.
// They are dropped rather than guessed at.
func parseClaudeToolProgress(content []byte) (map[string]interface{}, bool) {
	var frame claudeToolProgress
	if err := json.Unmarshal(content, &frame); err != nil {
		slog.Warn("invalid claude tool_progress JSON", "error", err)
		return nil, false
	}
	// A frame with no parent identifies no tool_use row, so nothing could carry its
	// badge. The field is nullable on the wire, and a null decodes to "".
	if frame.ParentToolUseID == "" {
		return nil, false
	}

	update := map[string]interface{}{
		contracts.RunningToolFieldSpanId: frame.ParentToolUseID,
	}
	switch {
	case frame.Heartbeat:
		// An unreadable elapsed time OMITS the key rather than shipping a 0. The
		// frontend merges each update into the span's entry, so an omitted key
		// keeps the last good value; a 0 means "not measured yet" there and would
		// blank a badge that already reads "1m 30s". The CLI computes the value
		// off the wall clock, so a backward clock step gives a negative one.
		if elapsed, ok := claudeNonNegativeCount(frame.ElapsedSeconds); ok {
			update[contracts.RunningToolFieldElapsedSeconds] = elapsed
		}
	case frame.SubagentType != "":
		update[contracts.RunningToolFieldRetry] = claudeSubagentRetryMap(frame.SubagentRetry)
	default:
		return nil, false
	}
	return update, true
}

// claudeSubagentRetryMap renders the retry state for the wire. A nil retry
// becomes an explicit nil (JSON null), which the frontend reads as "the retry
// resolved" -- distinct from an absent key, which leaves the state alone.
func claudeSubagentRetryMap(r *claudeSubagentRetry) interface{} {
	if r == nil {
		return nil
	}
	var errorStatus interface{}
	if r.ErrorStatus != nil {
		errorStatus = *r.ErrorStatus
	}
	return map[string]interface{}{
		contracts.RunningToolRetryFieldAttempt:       r.Attempt,
		contracts.RunningToolRetryFieldMaxRetries:    r.MaxRetries,
		contracts.RunningToolRetryFieldRetryDelayMs:  r.RetryDelayMs,
		contracts.RunningToolRetryFieldErrorStatus:   errorStatus,
		contracts.RunningToolRetryFieldErrorCategory: r.ErrorCategory,
	}
}

// claudeNonNegativeCount sanitizes a raw JSON number that Claude Code reports as
// a count, into a non-negative in-range int64. It reports ok=false when the wire
// value carries no usable number, so a caller can tell "the agent said zero"
// apart from "the agent said nothing this handler can read". Shared by the
// thinking-token estimate and the tool_progress elapsed time, so the rules below
// have one home rather than two copies that drift.
//
// The value is parsed leniently into a *float64 so a fractional or exponent form
// (`230.0`, `1.5e4`) still reads, then:
//   - an absent or malformed value, and a float64-overflowing one (1e400 ->
//     +Inf -> parse error), reports 0, false;
//   - a JSON `null` reports 0, false. The POINTER is what separates it from a
//     real 0: json.Unmarshal accepts `null` into a plain float64, returns no
//     error, and leaves the zero value, so a value target cannot tell the two
//     apart;
//   - a negative value reports 0, false (both a count and an elapsed time are
//     non-negative by definition, so a truncated -0.5 and a genuinely negative
//     wire value are both unusable) and no consumer ever sees a negative number;
//   - a NaN reports 0, false. encoding/json rejects a bare NaN literal, so no
//     Claude frame produces one, but the guard keeps int64(NaN) -- which is
//     undefined in Go -- off every path;
//   - a finite value at or above 2^63 (e.g. 1e300, which parses cleanly yet
//     saturates the int64 conversion to a garbage value) is out of range like
//     the overflowing 1e400, so it also reports 0, false rather than a nonsense
//     9.2-quintillion count.
//
// The two callers read the flag differently, which is why it exists. An elapsed
// time OMITS the wire key when ok is false: the frontend merges each update into
// the span's entry, so an omitted key keeps the last good elapsed time, while an
// explicit 0 means "not measured yet" and BLANKS a badge that reads "1m 30s".
// The thinking-token estimate keeps shipping the 0, because there a 0 is the
// clear signal the frontend acts on.
func claudeNonNegativeCount(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f *float64
	if err := json.Unmarshal(raw, &f); err != nil || f == nil {
		return 0, false
	}
	if math.IsNaN(*f) || *f < 0 || *f >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(*f), true
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
// claudeNonNegativeCount states what a malformed, negative, or out-of-range
// count becomes. Its "no usable number" flag is DISCARDED here, on purpose: an
// unreadable estimate broadcasts 0, and the frontend reads a 0 estimate as the
// signal to clear the counter. The elapsed-time caller omits its key instead,
// because there a 0 blanks a live value. See claudeNonNegativeCount.
func parseThinkingTokens(content []byte) (estimate int64, ok bool) {
	var msg struct {
		Subtype         string          `json:"subtype"`
		EstimatedTokens json.RawMessage `json:"estimated_tokens"`
	}
	if err := json.Unmarshal(content, &msg); err != nil || msg.Subtype != claudeSystemSubtypeThinkingTokens {
		return 0, false
	}
	estimate, _ = claudeNonNegativeCount(msg.EstimatedTokens)
	return estimate, true
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
	claimToken := a.sink.PersistControlRequest(cr.RequestID, content)
	a.sink.BroadcastControlRequest(cr.RequestID, content, claimToken)
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
		if mode == contracts.ClaudeModeAuto {
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
		contracts.RateLimitFieldRateLimitType: rlInfo.RateLimitType,
		contracts.RateLimitFieldStatus:        rlInfo.Status,
	}
	if rlInfo.Utilization != nil {
		tier[contracts.RateLimitFieldUtilization] = *rlInfo.Utilization
	}
	if rlInfo.ResetsAt != nil {
		tier[contracts.RateLimitFieldResetsAt] = *rlInfo.ResetsAt
	}
	if rlInfo.SurpassedThreshold != nil {
		tier[contracts.RateLimitFieldSurpassedThreshold] = *rlInfo.SurpassedThreshold
	}
	if rlInfo.OverageStatus != "" {
		tier[contracts.RateLimitFieldOverageStatus] = rlInfo.OverageStatus
	}
	if rlInfo.OverageResetsAt != nil {
		tier[contracts.RateLimitFieldOverageResetsAt] = *rlInfo.OverageResetsAt
	}
	if rlInfo.IsUsingOverage != nil {
		tier[contracts.RateLimitFieldIsUsingOverage] = *rlInfo.IsUsingOverage
	}

	a.sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRateLimits: map[string]any{rlInfo.RateLimitType: tier},
	})

	// Persist the raw `rate_limit_event` envelope verbatim as an
	// agent-emitted notification. The frontend's claudeRateLimitsFromMessage
	// (providers/claude/plugin.tsx) and claudeNotificationThreadEntry
	// (providers/claude/notifications.tsx) read `rate_limit_info` from this raw
	// Claude-native shape (camelCase) -- the persisted side stays in the SDK's
	// format so notification rendering remains a passthrough.
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content); err != nil {
		slog.Error("persist rate_limit notification", "agent_id", a.agentID, "error", err)
	}

	// Decide whether this event is an actual block that warrants waiting for a
	// reset. Only a hard "rejected" status blocks; "allowed" and
	// "allowed_warning" are both served -- a warning is just a heads-up that the
	// window is filling up -- so they clear any pending resume. This mirrors
	// Claude Code's own gate (it shows the blocking countdown only on "rejected")
	// and fixes a bug where any non-"allowed" status, notably "allowed_warning",
	// scheduled a spurious auto-continue.
	usingOverage := rlInfo.IsUsingOverage != nil && *rlInfo.IsUsingOverage
	blocked, resumeAt := claudeRateLimitResume(
		rlInfo.Status, rlInfo.ResetsAt, usingOverage, rlInfo.OverageStatus, rlInfo.OverageResetsAt)
	if !blocked {
		a.sink.CancelAutoContinue(AutoContinueReasonRateLimit)
		return
	}
	// Blocked, but a resume can only be scheduled if the event says when the
	// window lifts. A blocked-but-undated event leaves any existing schedule
	// intact rather than cancelling a legitimate pending resume.
	if resumeAt == nil {
		return
	}

	a.sink.ScheduleAutoContinue(AutoContinueSchedule{
		Reason:        AutoContinueReasonRateLimit,
		DueAt:         time.Unix(*resumeAt, 0).UTC(),
		SourcePayload: append([]byte(nil), rle.RateLimitInfo...),
	})
}

// claudeRateLimitStatusRejected is the only Claude rate_limit_event status that
// represents an actual block. The Anthropic-native status vocabulary is
// "allowed" / "allowed_warning" / "rejected" (and the same three for the overage
// status); "allowed" and "allowed_warning" are both served requests.
const claudeRateLimitStatusRejected = "rejected"

// claudeRateLimitResume reports whether a Claude rate_limit_event represents an
// actual block and, if so, the Unix reset time to auto-resume at (nil when the
// event is a block but carries no reset). Mirrors Claude Code's own gate:
//
//   - While NOT on overage, the base window blocks only on a "rejected" status,
//     lifting at resetsAt. "allowed" / "allowed_warning" are served.
//   - While ON overage, the base window is absorbed by the overage allowance, so
//     the block applies only once the overage itself is "rejected", lifting at
//     overageResetsAt.
func claudeRateLimitResume(status string, resetsAt *int64, usingOverage bool, overageStatus string, overageResetsAt *int64) (blocked bool, resumeAt *int64) {
	if usingOverage {
		if overageStatus == claudeRateLimitStatusRejected {
			return true, overageResetsAt
		}
		return false, nil
	}
	if status == claudeRateLimitStatusRejected {
		return true, resetsAt
	}
	return false, nil
}

// extractAndBroadcastUsage extracts token usage from assistant/result messages.
func (a *ClaudeCodeAgent) extractAndBroadcastUsage(env *messageEnvelope, msgType string) {
	info := map[string]interface{}{}
	if env.CostUSD != nil {
		info[contracts.SessionInfoKeyTotalCostUsd] = *env.CostUSD
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
		info[contracts.SessionInfoKeyContextUsage] = usageMap
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
		if targetMode == contracts.ClaudeModePlan && strings.Contains(resultLower, "entered plan mode") {
			confirmed = true
		} else if targetMode == contracts.ClaudeModeDefault && strings.Contains(resultLower, "approved your plan") {
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

var claudeSyntheticAPI5xxPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+5[0-9]{2}(?:$|[^[:alnum:]].*)`)
var claudeRetryableIdleTimeoutPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+Stream idle timeout(?:$|[^[:alnum:]].*)`)

// claudeRetryableOverloadedPattern matches the bare "Overloaded" result string.
// An overload is Anthropic HTTP 529 -- the most retryable error there is -- but
// Claude Code emits it in two forms: "API Error: 529 Overloaded" (with the
// numeric code, already caught by claudeSyntheticAPI5xxPattern) and a bare
// "API Error: Overloaded" with no code, which the 5xx pattern cannot match
// because it requires a three-digit code right after the punctuation. This
// pattern covers the code-less form so both spellings auto-continue.
var claudeRetryableOverloadedPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+Overloaded(?:$|[^[:alnum:]].*)`)

// isRetryableClaudeResultError reports whether a Claude result error should
// trigger auto-continue. Matching is case-insensitive (each pattern carries the
// (?i) flag): the result field is an unstructured, human-readable error string,
// not a structured code, so a cosmetic casing change must not silently regress
// the retry -- that over-strictness is exactly what left the bare "Overloaded"
// form unmatched before. False positives stay implausible regardless of casing
// because every pattern is anchored to the "API Error" prefix Claude Code emits.
func isRetryableClaudeResultError(s string) bool {
	return claudeSyntheticAPI5xxPattern.MatchString(s) ||
		claudeRetryableIdleTimeoutPattern.MatchString(s) ||
		claudeRetryableOverloadedPattern.MatchString(s)
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
