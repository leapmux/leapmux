package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/pathutil"
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
	claudeMsgTypeActiveGoal           = "active_goal"
	// claudeMsgTypeConversationReset is the command line interface's own word for
	// what `/clear` and a plan exit both do: they start a new conversation and keep
	// the session. LeapMux states the same fact as `context_cleared`, which every
	// other provider already writes and the transcript already draws.
	claudeMsgTypeConversationReset = "conversation_reset"

	// claudeControlRequestCanUseTool is the `request.subtype` of the control
	// request that escalates one tool call to the reader. It carries the
	// `tool_use_id` of the call it escalates, and the `agent_id` of the
	// subagent that made the call.
	claudeControlRequestCanUseTool = "can_use_tool"

	// claudePermissionCheckChecking is the `status` of a
	// `permission_check_status` frame while an automatic reviewer holds the
	// call. Only the Worker reads it, so it is not in the contract.
	claudePermissionCheckChecking = "checking"

	// claudeModelFallbackScopeLocal is the `scope` of a `model_refusal_fallback`
	// frame whose fallback model serves one response only. Only the Worker
	// reads it, so it is not in the contract.
	claudeModelFallbackScopeLocal   = "local"
	claudeLocalCommandCompact       = "compact"
	claudeLocalCommandOutcomeFailed = "failed"
)

// claudeHiddenSystemSubtypes is the set of `system` subtypes the transcript
// never shows. The contract table is the one list: the browser hides a stored
// row of each entry, and this set stops the Worker from storing one.
var claudeHiddenSystemSubtypes = func() map[string]struct{} {
	subtypes := make(map[string]struct{}, len(contracts.ClaudeHiddenSystemSubtypeKeys))
	for _, subtype := range contracts.ClaudeHiddenSystemSubtypeKeys {
		subtypes[subtype] = struct{}{}
	}
	return subtypes
}()

// Claude Code 2.1.302 declares these stdout types. Each supplies no transcript row.
var claudeDroppedFrameTypes = map[string]string{
	"stream_event":      "the launch disables partial-message frames",
	"tool_use_summary":  "the tool rows already state the calls",
	"prompt_suggestion": "the launch does not request prompt suggestions",
	"keep_alive":        "the heartbeat states no content",
	"auth_status":       "authentication runs outside this session",
	"autocompact_state": "the frame reports configuration, not a compaction boundary",
	"command_lifecycle": "the command lifecycle supplies no transcript content",
	"transcript_mirror": "the frame mirrors the native session file",
	"api_metrics":       "the frame supplies request telemetry",
}

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
// token usage is recorded yet, or when the 10s debounce window is not yet past
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
	usageMap := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
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
func (a *Agent) HandleOutput(content []byte) {
	a.handleClaudeOutput(content, "")
}

// handleClaudeOutput is the shared implementation. When msgType is empty, the
// type is parsed from the content; otherwise it uses the pre-parsed value from
// the output pipeline.
func (a *Agent) handleClaudeOutput(content []byte, msgType string) {
	if msgType == "" {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(content, &envelope); err != nil {
			slog.Warn("invalid agent output JSON", "agent_id", a.AgentID(), "error", err)
			return
		}
		msgType = envelope.Type
	}

	slog.Debug("HandleOutput", "agent_id", a.AgentID(), "type", msgType, "len", len(content))

	a.observeTurnFromOutput(content, msgType)
	a.resolveHeldAPIError(content, msgType)
	if msgType == claudeMsgTypeAssistant {
		var env messageEnvelope
		if json.Unmarshal(content, &env) == nil && env.IsAPIErrorMessage && env.ParentToolUseID == "" {
			var texts []string
			for _, block := range env.ContentBlocks() {
				if block.Type == "text" {
					texts = append(texts, block.Text)
				}
			}
			a.heldAPIErrorMu.Lock()
			a.heldAPIError = &claudeHeldAPIError{content: bytes.Clone(content), text: strings.Join(texts, "\n")}
			a.heldAPIErrorMu.Unlock()
			return
		}
	}

	switch msgType {
	case claudeMsgTypeAssistant, claudeMsgTypeSystem, claudeMsgTypeResult:
		if msgType == claudeMsgTypeSystem {
			// The init frame states which slash commands THIS build has, which
			// is what decides whether the session-goal controls are offered.
			a.observeSlashCommands(content)
		}
		a.handlePersistableMessage(content, msgType)

	case claudeMsgTypeUser:
		if !isSimpleUserTextEcho(content) {
			a.handlePersistableMessage(content, msgType)
		}

	case contracts.NotificationTypeContextCleared, contracts.NotificationTypeInterrupted, contracts.NotificationTypePlanExecution:
		if msgType == contracts.NotificationTypeInterrupted {
			a.sink.ResetSpans()
		}
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: content}); err != nil {
			slog.Error("persist agent notification", "agent_id", a.AgentID(), "type", msgType, "error", err)
		}

	case claudeMsgTypeControlRequest:
		a.claudeCodeHandleControlRequest(content)

	case claudeMsgTypeControlCancelRequest:
		a.claudeCodeHandleControlCancel(content)

	case claudeMsgTypeControlResponse:
		a.claudeCodeHandleControlResponse(content)

	case contracts.NotificationTypeRateLimitEvent:
		a.claudeCodeHandleRateLimitEvent(content)

	case claudeMsgTypeToolProgress:
		a.claudeHandleToolProgress(content)

	case claudeMsgTypeActiveGoal:
		a.handleActiveGoal(content)

	case claudeMsgTypeConversationReset:
		a.claudeHandleConversationReset()

	default:
		if reason, dropped := claudeDroppedFrameTypes[msgType]; dropped {
			slog.Debug("claude output needs no transcript row", "agent_id", a.AgentID(), "type", msgType, "reason", reason)
			return
		}
		if msgType == "" {
			slog.Debug("claude output has no type", "agent_id", a.AgentID())
			return
		}
		a.persistUnknownClaudeFrame(content)
	}
}

type claudeHeldAPIError struct {
	content []byte
	text    string
}

// resolveHeldAPIError compares a held root error with the next root conversation frame.
// System frames and synthetic context do not resolve the hold.
func (a *Agent) resolveHeldAPIError(content []byte, msgType string) {
	if msgType != claudeMsgTypeAssistant && msgType != claudeMsgTypeUser && msgType != claudeMsgTypeResult {
		return
	}
	var env messageEnvelope
	if json.Unmarshal(content, &env) != nil || env.ParentToolUseID != "" || claudeIsSyntheticContext(msgType, &env) {
		return
	}
	a.heldAPIErrorMu.Lock()
	held := a.heldAPIError
	a.heldAPIError = nil
	a.heldAPIErrorMu.Unlock()
	if held == nil {
		return
	}
	if msgType == claudeMsgTypeResult && env.IsError && env.Result == held.text {
		return
	}
	a.handlePersistableMessage(held.content, claudeMsgTypeAssistant)
}

// flushHeldAPIError stores an error when the process exits without a matching result.
func (a *Agent) flushHeldAPIError() {
	a.heldAPIErrorMu.Lock()
	held := a.heldAPIError
	a.heldAPIError = nil
	a.heldAPIErrorMu.Unlock()
	if held != nil {
		a.handlePersistableMessage(held.content, claudeMsgTypeAssistant)
	}
}

// persistUnknownClaudeFrame keeps an unknown frame only when it identifies the current session.
func (a *Agent) persistUnknownClaudeFrame(content []byte) {
	sessionID := providerkit.StringField(content, "session_id")
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" || sessionID != current {
		slog.Debug("unknown claude output does not identify the current session", "agent_id", a.AgentID(), "session_id", sessionID)
		return
	}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content}, agent.SpanInfo{}); err != nil {
		slog.Error("persist unknown claude output", "agent_id", a.AgentID(), "error", err)
	}
}

// claudeHandleConversationReset persists the neutral context-cleared notice.
//
// The interface's own frame carries a `new_conversation_id`, which identifies
// the conversation the CLI moved to and which nothing here follows: LeapMux
// keeps ONE agent session across a clear, and the new id would only give the
// transcript a second name for the same session. What a reader needs is the
// boundary, which is exactly what `context_cleared` states.
func (a *Agent) claudeHandleConversationReset() {
	notice, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeContextCleared})
	if err != nil {
		slog.Error("build claude context-cleared notice", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: notice}); err != nil {
		slog.Error("persist agent notification", "agent_id", a.AgentID(), "type", contracts.NotificationTypeContextCleared, "error", err)
	}
}

// contentBlock represents a single block in message.content[].
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
}

// messageEnvelope is the shared top-level structure parsed once for
// assistant, user, system, and result messages.
type messageEnvelope struct {
	LocalCommandRun struct {
		Command string `json:"command"`
	} `json:"local_command_run"`
	LocalCommandOutcome struct {
		Kind string `json:"kind"`
	} `json:"local_command_outcome"`
	IsAPIErrorMessage bool   `json:"is_api_error_message"`
	IsSynthetic       bool   `json:"isSynthetic"`
	UUID              string `json:"uuid"`
	ParentToolUseID   string `json:"parent_tool_use_id"`
	ToolUseID         string `json:"tool_use_id"`
	TaskDescription   string `json:"task_description"`
	Origin            struct {
		Kind         string `json:"kind"`
		From         string `json:"from"`
		SenderTaskID string `json:"senderTaskId"`
		Body         string `json:"body"`
		Handback     bool   `json:"handback"`
		Flagged      bool   `json:"flagged"`
	} `json:"origin"`
	Message struct {
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
	// TerminalReason is the CLI's own statement of why the turn ended, on a
	// `result`. Claude Code 2.1.289 writes it on every `result` of a turn. A frame
	// from a build that does not write it leaves the field empty.
	TerminalReason string `json:"terminal_reason"`

	// contentBlocks is lazily populated from RawContent.
	contentBlocks []contentBlock
	contentParsed bool
}

// The `terminal_reason` values that state a turn ended in an abort, as the CLI's own
// vocabulary spells them. Claude Code 2.1.289 writes the first when a stop arrives while
// the model streams, and the second when it arrives while a tool runs.
const (
	claudeTerminalReasonAbortedStreaming = "aborted_streaming"
	claudeTerminalReasonAbortedTools     = "aborted_tools"
)

// statesAbortedTurn reports whether a `result` states that the stop took effect, so
// the turn ended in an abort. A turn that ended some other way before the CLI read the
// stop keeps its own outcome.
//
// `terminal_reason` decides when the frame states it. It outranks `subtype` and
// `is_error`, because neither tells an abort from a failure: an abort and a failed tool
// both write `subtype: error_during_execution`, and an API failure writes
// `subtype: success` with `is_error: true`. A turn that finished writes
// `terminal_reason: completed`, and an API failure writes `api_error`.
//
// A `result` that states no reason falls back to `is_error`: `true` reads as an abort,
// and `false` reads as a turn that ended without one. The fallback covers a frame that
// omits the field. Qoder also states no reason for an abort of the model, and its
// provider reads `is_error` the same way.
func (e *messageEnvelope) statesAbortedTurn() bool {
	switch e.TerminalReason {
	case claudeTerminalReasonAbortedStreaming, claudeTerminalReasonAbortedTools:
		return true
	case "":
		return e.IsError
	default:
		return false
	}
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
func (a *Agent) processAssistantBlocks(env *messageEnvelope) {
	// Determine the parent span for any Agent tool_use blocks.
	parentSpanID := env.ParentToolUseID

	// A SendMessage here may be about to restart a finished subagent. Record the
	// recipient before the tool runs, because the task_started it produces cannot
	// be told from a session resume's hydration burst on its own. This is the
	// ROOT transcript, so the arms are scoped to it ("").
	a.claudeArmRestartsFromBlocks(env, "")

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
				slog.Warn("claude tool input unmarshal failed", "agent_id", a.AgentID(), "tool", block.Name, "error", err)
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
					a.sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(planContentStr))
				}
			}
		}
	}

	if toolUseCount > 0 {
		a.Mu.Lock()
		a.TurnToolUses += toolUseCount
		a.Mu.Unlock()
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
func claudeSpanInfoFor(sink agent.SpanServices, msgType string, env *messageEnvelope, parentSpanID string) agent.SpanInfo {
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
	return agent.SpanInfo{
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
func claudeCloseToolResultSpans(sink agent.SpanServices, env *messageEnvelope) {
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
func (a *Agent) handlePersistableMessage(content []byte, msgType string) {
	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	if msgType == claudeMsgTypeUser {
		source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	}

	if msgType == claudeMsgTypeSystem {
		subtype := providerkit.StringField(content, "subtype")
		// Select the native handler before it decodes its payload fields.
		// An unrelated malformed field must not affect this event's handler.
		switch subtype {
		case contracts.ClaudeSystemSubtypeStatus:
			a.handleClaudeStatus(content)
			return
		case contracts.ClaudeSystemSubtypeCompactBoundary, contracts.ClaudeSystemSubtypeMicrocompactBoundary:
			a.endClaudeCompaction()
		case contracts.ClaudeHiddenSystemSubtypeThinkingTokens:
			a.handleThinkingTokens(content)
			return
		case contracts.ClaudeHiddenSystemSubtypePermissionCheckStatus:
			a.handlePermissionCheckStatus(content)
			return
		case contracts.ClaudeHiddenSystemSubtypeTaskStarted,
			contracts.ClaudeHiddenSystemSubtypeTaskProgress,
			contracts.ClaudeHiddenSystemSubtypeTaskNotification,
			contracts.ClaudeHiddenSystemSubtypeTaskUpdated,
			contracts.ClaudeHiddenSystemSubtypeBackgroundTasksChanged,
			contracts.ClaudeHiddenSystemSubtypeSessionStateChanged:
			a.claudeHandleTaskEvent(content)
			return
		}

		// Three hidden subtypes change state before they return.
		switch subtype {
		case contracts.ClaudeHiddenSystemSubtypeInit:
			// The init frame states the session and which slash commands THIS
			// build has (observeSlashCommands already read the latter on every
			// system frame). ONLY init moves the session: the side-event queue
			// stamps session_id on every frame it writes, and firing session
			// init from each one would re-broadcast the same session at every
			// step of a turn.
			a.claudeCodeHandleSystemInit(content)
			return
		case contracts.ClaudeHiddenSystemSubtypeCommandsChanged:
			a.observeChangedCommands(content)
			return
		case contracts.ClaudeHiddenSystemSubtypeElicitationComplete:
			a.handleElicitationComplete(content)
			return
		}

		// The table of system subtypes the transcript never shows. A frame of
		// one is consumed or dropped here, so no row exists for the browser to
		// hide. An UNKNOWN subtype still persists: a stored no-display card is
		// the signal that a new frame exists.
		if _, hidden := claudeHiddenSystemSubtypes[subtype]; hidden {
			return
		}

		switch subtype {
		case contracts.ClaudeSystemSubtypePermissionDenied:
			// The denial persists (the transcript draws "Permission denied:
			// ..."), and it ends the live permission check of the call it
			// identifies: a host must stop showing the check at any frame for
			// the call but its own status. A subagent's denial persists in that
			// subagent's transcript, so the handler persists it and returns.
			if a.handlePermissionDenied(content) {
				return
			}
		case contracts.ClaudeSystemSubtypeInformational:
			// Claude shows level info only in its transcript mode. The worker stores no row for that level.
			if providerkit.StringField(content, "level") == contracts.ClaudeInformationalLevelInfo {
				return
			}
		case contracts.ClaudeSystemSubtypeHookResponse:
			// A hook that blocks a call reports itself in that call's own
			// result. Only a FAILED hook needs a row of its own; the rest would
			// add one row per hook per turn with nothing new to state.
			if providerkit.StringField(content, "outcome") != contracts.ClaudeHookOutcomeError {
				return
			}
		case contracts.ClaudeSystemSubtypeModelRefusalFallback:
			// The session model follows the fallback unless the swap is
			// request-local: `scope: "local"` serves one response and leaves the
			// session model unchanged, and an ABSENT scope is the session-scoped
			// swap ("Absent from older CLIs (treat as 'session')" in the
			// interface's own schema). The row itself persists either way.
			//
			// The frame also lists `retracted_message_uuids`, the refused partial
			// messages that the CLI removed. LeapMux keeps those rows for now:
			// https://github.com/leapmux/leapmux/issues/533
			// An absent scope means a session fallback. Only local opts out.
			if providerkit.StringField(content, "scope") != claudeModelFallbackScopeLocal {
				a.adoptClaudeModelFallback(content)
			}
		case contracts.ClaudeSystemSubtypeModelConsentFallback:
			// The consent check swaps the session off the requested model, so the
			// session model follows the fallback. The row itself persists.
			a.adoptClaudeModelFallback(content)
		}

		if claudeIsConsolidatable(content) {
			// Keep the native notification unchanged. The browser reads its display fields.
			if _, err := a.sink.PersistNotification(source, agent.MessageContent{Original: content}); err != nil {
				slog.Error("persist notification-threaded system message", "agent_id", a.AgentID(), "error", err)
			} else if subtype == contracts.ClaudeSystemSubtypeApiRetry {
				a.apiRetry.NoteRetry()
			}
			return
		}
	}

	// Parse the message envelope once for all downstream consumers.
	var env messageEnvelope
	if err := json.Unmarshal(content, &env); err != nil {
		slog.Warn("invalid message envelope", "agent_id", a.AgentID(), "error", err)
		return
	}
	if claudeIsSyntheticContext(msgType, &env) {
		return
	}
	if msgType == claudeMsgTypeAssistant && env.ParentToolUseID == "" && claudeIsFailedCompactCommand(&env) {
		return
	}
	if msgType == claudeMsgTypeResult && env.ParentToolUseID == "" {
		a.endClaudeCompaction()
	}

	if msgType == claudeMsgTypeUser {
		a.settleToolControls(env.ContentBlocks())
	}

	// Route forwarded output into the child's transcript through parent_tool_use_id.
	// Claude Code 2.1.302 forwards assistant output and tool results, including synthetic API errors.
	// It sends no child result. Keep that route for a build that forwards one.
	if env.ParentToolUseID != "" && (msgType == claudeMsgTypeAssistant ||
		msgType == claudeMsgTypeUser || msgType == claudeMsgTypeResult) {
		a.routeSubagentMessage(content, msgType, &env)
		return
	}
	if msgType == claudeMsgTypeResult && a.persistClaudePeerHandbackResult(&env) {
		return
	}
	if msgType == claudeMsgTypeUser {
		a.persistClaudeHandbackResult(&env)
	}
	if msgType == claudeMsgTypeAssistant && !env.IsAPIErrorMessage {
		a.apiRetry.Resolve(a.sink)
	}
	if msgType == claudeMsgTypeResult {
		a.apiRetry.Reset()
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

	messageContent := agent.MessageContent{Original: content}
	if msgType == claudeMsgTypeResult {
		messageContent = a.MessageWithToolUses(content)
		a.Mu.Lock()
		a.TurnToolUses = 0
		a.Mu.Unlock()
		// One `result` ends one turn, so it spends the note whatever it states. The
		// note marks the turn end only when the frame also states that the stop took
		// effect: a turn that ended before the CLI read the stop keeps its outcome.
		stopRequested := a.takeInterruptRequest()
		if stopRequested && env.statesAbortedTurn() {
			messageContent.Completion = agent.MessageCompletionInterrupted
		}
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
		// PersistTurnEnd stores the final envelope and broadcasts the Git status.
		persistErr = a.sink.PersistTurnEnd(messageContent, spanInfo)
	} else {
		persistErr = a.sink.PersistMessage(source, messageContent, spanInfo)
	}
	if persistErr != nil {
		slog.Error("persist agent message", "agent_id", a.AgentID(), "error", persistErr)
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
		// The root result ends this response. Native session state can keep the turn active for a queued steer.
		// A build that forwards a child result routes it above, so that result cannot change the root turn.
		// PersistTurnEnd already recorded the tool-call count. Interrupted and failed responses use this path also.
		providerkit.ScheduleOrCancelServerErrorAutoRetry(a.sink, env.IsError && isRetryableClaudeResultError(env.Result), content)

		// Reset all span tracking so the next turn starts clean.
		a.sink.ResetSpans()
		// Drop the ROOT's SendMessage restart arms with it. A restart's task_started
		// lands inside the turn that sent the message (the tool awaits the
		// restart), so an arm still standing here addressed a live subagent, a
		// recipient outside this session, or a send the CLI refused -- and none of
		// those will ever fire it.
		//
		// Only the root's. A subagent runs past this boundary and clears its own
		// arms at its own turn end, so wiping every arm here would drop a live
		// subagent's before its task_started arrived.
		a.tasks.clearClaudeRestarts("")
		// A CLI with session-state reports keeps the turn active until idle.
		// The fallback clears it here, after its spans reset, because that clear releases the queue.
		if a.publishesSessionState() {
			a.recordClaudeResultEdge()
		} else {
			a.disarmTurn()
		}
	}
}

// claudeIsSyntheticContext identifies native context that the user did not send.
// A synthetic tool result still settles its control and span.
func claudeIsSyntheticContext(msgType string, env *messageEnvelope) bool {
	return msgType == claudeMsgTypeUser && env.IsSynthetic && !hasToolResultBlock(env)
}

// claudeIsFailedCompactCommand identifies the manual error that the final status already states.
func claudeIsFailedCompactCommand(env *messageEnvelope) bool {
	return env.LocalCommandRun.Command == claudeLocalCommandCompact && env.LocalCommandOutcome.Kind == claudeLocalCommandOutcomeFailed
}

// handleClaudeStatus publishes a compaction start and stores only a failed end.
func (a *Agent) handleClaudeStatus(content []byte) {
	failed := providerkit.StringField(content, "compact_result") == contracts.ClaudeCompactResultFailed
	if providerkit.StringField(content, "status") == contracts.ClaudeStatusCompacting && !failed {
		a.Mu.Lock()
		started := !a.compactionLive
		a.compactionLive = true
		a.Mu.Unlock()
		if started {
			providerkit.PublishCompaction(a.sink, "")
		}
		return
	}
	a.endClaudeCompaction()
	if failed {
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content}); err != nil {
			slog.Error("persist failed claude compaction", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// endClaudeCompaction clears a live start once, before the stored end.
func (a *Agent) endClaudeCompaction() {
	a.Mu.Lock()
	live := a.compactionLive
	a.compactionLive = false
	a.Mu.Unlock()
	if live {
		providerkit.EndCompaction(a.sink)
	}
}

// handleThinkingTokens intercepts Claude Code's `system`/`thinking_tokens`
// telemetry. Claude Code emits the line during extended thinking. It carries a
// running `estimated_tokens` count for the turn and streams frequently. Like a
// thinking-text delta, it is live progress rather than timeline content.
//
// When the line is a thinking-token update, the handler reports the latest
// estimate as progress, which the sink broadcasts over the ephemeral
// agent_session_info channel (seq -1, never written to the messages table). It
// then returns true, so the caller persists nothing. It returns false for any
// other system message, which continues down the normal persist path.
//
// The subtype shares the literal "thinking_tokens" with
// contracts.SessionInfoKeyThinkingTokens, the agent_session_info key of the
// estimate. The two are different concepts: a Claude wire `subtype` and a
// platform session-info key. Each one keeps its own constant, so a change to
// one does not force a change to the other.
func (a *Agent) handleThinkingTokens(content []byte) bool {
	estimate, ok := parseThinkingTokens(content)
	if !ok {
		return false
	}
	if estimate <= 0 {
		a.sink.ReportProgress(agent.CompleteModelProgress("claude:thinking"))
	} else {
		a.sink.ReportProgress(agent.NativeTokenProgress("claude:thinking", estimate))
	}
	return true
}

// handlePermissionCheckStatus intercepts Claude Code's
// `system`/`permission_check_status` frame. It broadcasts the live permission
// check state of the call that the frame identifies. The broadcast goes over
// the ephemeral agent_session_info channel. It returns true, so the caller
// persists nothing. The tool card shows the badge, so a stored row would only
// draw a no-display card for a fact that the card already states. It returns
// false for any other system message.
//
// A frame that states an `agent_id` resolves to the child sink of that task,
// so a subagent's call gets its badge in the subagent's own tool card. An
// unknown child consumes the frame and broadcasts nothing.
func (a *Agent) handlePermissionCheckStatus(content []byte) bool {
	toolUseID, agentID, status, ok := parsePermissionCheckStatus(content)
	if !ok {
		return false
	}
	sink, resolved := a.claudePermissionCheckSink(agentID)
	if !resolved {
		return true
	}
	// `done`, and any status that this build does not know, ends the wait. The
	// badge must clear at the earliest signal, so an unknown word is read as
	// its end rather than as a reason to keep it standing.
	providerkit.PublishPermissionCheck(sink, toolUseID, status == claudePermissionCheckChecking)
	return true
}

// parsePermissionCheckStatus reads the fields the permission-check state
// broadcast needs from a `permission_check_status` line, and reports ok=false
// for every other line. Pure (no sink, no I/O) so the routing rule is
// unit-testable directly.
func parsePermissionCheckStatus(content []byte) (toolUseID, agentID, status string, ok bool) {
	var msg struct {
		Subtype   string `json:"subtype"`
		ToolUseID string `json:"tool_use_id"`
		AgentID   string `json:"agent_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(content, &msg); err != nil || msg.Subtype != contracts.ClaudeHiddenSystemSubtypePermissionCheckStatus {
		return "", "", "", false
	}
	return msg.ToolUseID, msg.AgentID, msg.Status, true
}

// adoptClaudeModelFallback folds the fallback model of a model-swap frame into
// the session's model, so the settings the reader sees follow the swap the row
// reports.
func (a *Agent) adoptClaudeModelFallback(content []byte) {
	var msg struct {
		FallbackModel string `json:"fallback_model"`
	}
	if err := json.Unmarshal(content, &msg); err != nil || msg.FallbackModel == "" {
		return
	}
	model := normalizeClaudeCodeModel(msg.FallbackModel)
	a.Mu.Lock()
	a.model = model
	a.Mu.Unlock()
	a.sink.PersistSettingsRefresh(map[string]string{agent.OptionIDModel: model})
}

// claudePermissionCheckSink resolves the sink of the agent that made a tool
// call. Claude Code identifies that agent by its `agent_id`, the task id of the
// subagent. A signal with no `agent_id` describes the root. A signal that
// identifies an unknown child resolves to nothing, and ok reports that.
//
// The permission-check broadcasts and the subagent's permission denial share
// this one rule, so a badge and the denial of its call always reach the same
// transcript.
func (a *Agent) claudePermissionCheckSink(agentID string) (agent.ProviderServices, bool) {
	if agentID == "" {
		return a.sink, true
	}
	known := lookupClaudeKnownTask(a.sink, agentID)
	if !known.exists || known.childID == "" {
		return nil, false
	}
	return a.sink.ChildSink(known.childID), true
}

// handlePermissionDenied ends the live permission check of a denied call, and
// persists the denial in the transcript of the agent that made the call. It
// reports true when it persisted the row itself.
//
// A denial that states the `agent_id` of a known task came from a subagent's
// call. The row of that call is in the subagent's transcript, so the denial
// persists there, under the span of the call. A denial with no `agent_id`
// returns false, and the caller persists it in the root transcript. A denial
// whose `agent_id` identifies no known task also returns false: no child
// transcript can take the row, and the root keeps it rather than losing it. No
// known sink holds a badge for that call, so it clears nothing.
func (a *Agent) handlePermissionDenied(content []byte) bool {
	var msg struct {
		ToolUseID string `json:"tool_use_id"`
		AgentID   string `json:"agent_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return false
	}
	sink, resolved := a.claudePermissionCheckSink(msg.AgentID)
	if !resolved {
		return false
	}
	providerkit.PublishPermissionCheck(sink, msg.ToolUseID, false)
	if msg.AgentID == "" {
		return false
	}
	// The denial sits under the span of the call that it denies, as a root
	// denial does: a system frame that states a tool_use_id and no
	// parent_tool_use_id takes that tool_use_id as its parent span.
	span := agent.SpanInfo{ParentSpanID: msg.ToolUseID}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content}, span); err != nil {
		slog.Warn("persist claude subagent permission denial", "agent_id", a.AgentID(), "task_id", msg.AgentID, "error", err)
	}
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
func (a *Agent) claudeHandleToolProgress(content []byte) {
	// The update states the session that the root sink stamps on its rows. The
	// permission-check broadcast reads its session from its sink the same way,
	// so the updates of one call address the same browser entry.
	update, ok := parseClaudeToolProgress(content, a.sink.MessageSessionID())
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
// sessionID is the provider session that runs the tool. The update carries it
// beside the span id because the two TOGETHER address the tool_use row: a span
// id is unique inside one session, not across every session of one agent. The
// browser keys its live entry by the pair, so a frame that stated no session
// would leave the badge unreachable from the row that owns it.
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
func parseClaudeToolProgress(content []byte, sessionID string) (map[string]interface{}, bool) {
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
		contracts.RunningToolFieldSpanId:         frame.ParentToolUseID,
		contracts.RunningToolFieldAgentSessionId: sessionID,
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
// would make json.Unmarshal fail on a malformed or out-of-range count -- a
// quoted "230", an overflowing 1e400 -- and return ok=false. The line would then
// report no progress: the hidden-subtype table drops it, and the counter keeps a
// stale estimate instead of the clear that an unreadable count sends. Matching
// the subtype first separates "is this a thinking_tokens line?" from "did the
// count parse?".
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
	if err := json.Unmarshal(content, &msg); err != nil || msg.Subtype != contracts.ClaudeHiddenSystemSubtypeThinkingTokens {
		return 0, false
	}
	estimate, _ = claudeNonNegativeCount(msg.EstimatedTokens)
	return estimate, true
}

// claudeCodeHandleSystemInit extracts session_id from system init messages.
func (a *Agent) claudeCodeHandleSystemInit(content []byte) {
	var initMsg struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(content, &initMsg); err != nil || initMsg.SessionID == "" {
		return
	}
	a.Mu.Lock()
	expected := a.sessionID
	a.sessionID = initMsg.SessionID
	a.Mu.Unlock()
	if expected != "" && expected != initMsg.SessionID {
		slog.Warn("Claude Code reported a different session ID", "agent_id", a.AgentID(), "expected_session_id", expected, "session_id", initMsg.SessionID)
	}
	a.sink.UpdateSessionID(initMsg.SessionID)
	a.sink.BroadcastStatusActive(initMsg.SessionID)
}

// claudeControlRequestFields holds the optional fields of a control_request's
// `request` object that the Worker reads beside the request id.
type claudeControlRequestFields struct {
	Subtype string
	// ToolUseID identifies the call that a `can_use_tool` escalates.
	ToolUseID string
	// AgentID is the task id of the subagent that made the call. It is empty
	// for a call of the root agent.
	AgentID string
	// MCPServerName and ElicitationID identify one server's elicitation.
	MCPServerName string
	ElicitationID string
}

// parseClaudeControlRequestFields reads the optional fields of a
// control_request's `request` object. Each field decodes on its own: a field
// that is absent, null, or of the wrong type reads as "", and it costs only
// itself. A `request` that is not an object reads as no fields at all.
func parseClaudeControlRequestFields(content []byte) claudeControlRequestFields {
	var frame struct {
		Request map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(content, &frame); err != nil {
		return claudeControlRequestFields{}
	}
	var fields claudeControlRequestFields
	fields.Subtype, _ = providerkit.RawString(frame.Request["subtype"])
	fields.ToolUseID, _ = providerkit.RawString(frame.Request["tool_use_id"])
	fields.AgentID, _ = providerkit.RawString(frame.Request["agent_id"])
	fields.MCPServerName, _ = providerkit.RawString(frame.Request["mcp_server_name"])
	fields.ElicitationID, _ = providerkit.RawString(frame.Request["elicitation_id"])
	return fields
}

// claudeCodeHandleControlCancel retires the exact pending publication.
func (a *Agent) claudeCodeHandleControlCancel(content []byte) {
	var cc struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(content, &cc); err != nil {
		slog.Warn("invalid control_cancel_request JSON", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.retireControl(cc.RequestID)
}

// handleElicitationComplete answers and withdraws the elicitation control
// request that the frame completes. An unknown elicitation id, and a request
// that the reader answered already, do nothing.
//
// The MCP server confirmed that the out-of-band flow of a URL-mode elicitation
// finished, so the reader acted on the URL. Claude Code 2.1.302's headless
// completion handler only logs the completion and writes this frame. Its
// elicitation handler still waits for the host's answer, so the MCP call stays
// blocked until the Worker answers. The Worker therefore accepts the request,
// as the browser does for a reader who accepts it (buildElicitationResponse in
// frontend/src/components/chat/controls/elicitationForm.ts).
//
// The answer is automatic, so finishControlAnswer withdraws the reader's card,
// which is stale now. retireControl then removes the record. It also
// withdraws the card when the write fails, because the request can get no
// other answer.
func (a *Agent) handleElicitationComplete(content []byte) {
	var msg struct {
		MCPServerName string `json:"mcp_server_name"`
		ElicitationID string `json:"elicitation_id"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return
	}
	requestID, record := a.claimElicitation(msg.MCPServerName, msg.ElicitationID)
	if record == nil {
		return
	}
	answer, err := json.Marshal(map[string]any{
		"type": claudeMsgTypeControlResponse,
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{"action": contracts.MCPElicitationActionAccept},
		},
	})
	if err == nil {
		err = a.Process.SendRawInput(answer, agent.StopContext{})
	}
	a.finishControlAnswer(requestID, record, err)
	if err != nil {
		slog.Warn("send claude elicitation answer", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
	}
	a.retireControl(requestID)
}

// claudeCodeHandleControlResponse settles a reply that arrived after its synchronous wait.
func (a *Agent) claudeCodeHandleControlResponse(content []byte) {
	var cr struct {
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
			Response  struct {
				Mode string `json:"mode"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &cr); err != nil || cr.Response.RequestID == "" || cr.Response.Subtype != "success" && cr.Response.Subtype != "error" {
		return
	}
	result := claudeCodeControlResult{Success: cr.Response.Subtype == "success", Mode: cr.Response.Response.Mode, Error: cr.Response.Error}
	var err error
	if !result.Success {
		reason := result.Error
		if reason == "" {
			reason = "Claude Code refused the permission mode"
		}
		err = errors.New(reason)
	}
	a.Mu.Lock()
	change := a.permissionModeChange
	matched := change != nil && change.requestID == cr.Response.RequestID
	if matched {
		a.completePermissionModeLocked(change, result, err, true)
	}
	mode := a.permissionModeLocked()
	a.Mu.Unlock()
	if matched {
		a.sink.UpdatePermissionMode(mode)
		a.queuePermissionApprovals()
	}
}

// claudeCodeHandleRateLimitEvent broadcasts rate_limit_event and persists as notification.
func (a *Agent) claudeCodeHandleRateLimitEvent(content []byte) {
	var rle struct {
		RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	}
	if err := json.Unmarshal(content, &rle); err != nil || len(rle.RateLimitInfo) == 0 {
		return
	}

	// Convert the SDK's camelCase fields to the session-info snake_case fields.
	// Add each new SDK field here explicitly. The stored notification retains the raw Claude fields for other readers.
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
		slog.Warn("claude rate limit info unmarshal failed", "agent_id", a.AgentID(), "error", err)
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
		contracts.SessionInfoKeyRateLimits: map[string]any{
			contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeMerge,
			contracts.RateLimitUpdateFieldValues: map[string]any{rlInfo.RateLimitType: tier},
		},
	})

	// Persist the raw `rate_limit_event` envelope verbatim as an
	// agent-emitted notification. The frontend's claudeRateLimitsFromMessage
	// (providers/claude/plugin.tsx) and claudeNotificationThreadEntry
	// (providers/claude/extractors/notification.ts) read `rate_limit_info` from this raw
	// Claude-native shape (camelCase) -- the persisted side stays in the SDK's
	// format so notification rendering remains a passthrough.
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content}); err != nil {
		slog.Error("persist rate_limit notification", "agent_id", a.AgentID(), "error", err)
	}

	// Decide whether this event is an actual block that warrants waiting for a
	// reset. Only a hard "rejected" status blocks; "allowed" and
	// "allowed_warning" are both served -- a warning only states that the window
	// fills -- so they clear any pending resume. This mirrors Claude Code's own
	// block rule (it shows the blocking countdown only on "rejected")
	// and fixes a bug where any non-"allowed" status, notably "allowed_warning",
	// scheduled a spurious automatic retry.
	usingOverage := rlInfo.IsUsingOverage != nil && *rlInfo.IsUsingOverage
	blocked, resumeAt := claudeRateLimitResume(
		rlInfo.Status, rlInfo.ResetsAt, usingOverage, rlInfo.OverageStatus, rlInfo.OverageResetsAt)
	if !blocked {
		a.sink.CancelUsageLimitRetry()
		return
	}
	// Blocked, but a resume can only be scheduled if the event says when the
	// window lifts. A blocked-but-undated event leaves any existing schedule
	// intact rather than cancelling a legitimate pending resume.
	if resumeAt == nil {
		return
	}

	a.sink.ScheduleUsageLimitRetry(time.Unix(*resumeAt, 0).UTC(), append([]byte(nil), rle.RateLimitInfo...))
}

// claudeRateLimitStatusRejected is the only Claude rate_limit_event status that
// represents an actual block. The Anthropic-native status vocabulary is
// "allowed" / "allowed_warning" / "rejected" (and the same three for the overage
// status); "allowed" and "allowed_warning" are both served requests.
const claudeRateLimitStatusRejected = "rejected"

// claudeRateLimitResume reports whether a Claude rate_limit_event represents an
// actual block and, if so, the Unix reset time to auto-resume at (nil when the
// event is a block but carries no reset). Mirrors Claude Code's own block rule:
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
func (a *Agent) extractAndBroadcastUsage(env *messageEnvelope, msgType string) {
	info := map[string]interface{}{}
	if env.CostUSD != nil {
		info[contracts.SessionInfoKeyTotalCostUsd] = *env.CostUSD
	}

	// Capture a.model and the effort resolver in one a.Mu critical section.
	// This handler runs on readOutputLoop while refreshSettingsFromAgent can change a.model under the same mutex.
	// The resolver captures a.availableModels. Startup builds that catalog with convertClaudeModels, then may insert an entry with ensureSettledModelListed.
	// No later operation changes the catalog.
	//
	// Assistant and result output require a user turn. The manager registers this agent before a user turn can reach it.
	// The registration lock publishes every startup write before this handler reads the catalog.
	// The resolver needs a.Mu for the paired model read only. Immutable catalog entries permit the window lookup after the mutex releases.
	a.Mu.Lock()
	model := a.model
	resolver := a.effortResolver()
	a.Mu.Unlock()
	// The catalog estimates the window from the model ID: "[1m]" means 1M, otherwise 200K.
	// resolver.contextWindow reads the dynamic catalog first, then the static fallback, as the effort lookups do.
	// A resumed model that the live CLI omits can therefore retain its known window.
	// Zero means neither catalog supplies a window, including an unresolved account-default sentinel or a model absent from both catalogs.
	// The broadcast omits context_window for zero. The frontend also shows no window when it cannot resolve one.
	//
	// For a concrete model absent from both catalogs, result modelUsage supplies the real window through findPrimaryContextWindow.
	// The "default" sentinel matches no concrete usage key, so its window stays unknown.
	// A later refreshSettingsFromAgent can resolve that sentinel. Its model change initializes the window again here.
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

	if usageMap, ok := snapshot.buildBroadcast(msgType, a.Clock().Now()); ok {
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
func (a *Agent) getOrCreateUsageSnapshot() *contextUsageSnapshot {
	if a.contextUsage == nil {
		a.contextUsage = &contextUsageSnapshot{}
	}
	return a.contextUsage
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
func (a *Agent) detectPlanModeFromToolResult(env *messageEnvelope) {
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
				"agent_id", a.AgentID(),
				"tool_use_id", block.ToolUseID,
				"mode", targetMode)
			a.sink.UpdatePermissionMode(targetMode)
		} else {
			truncated := resultText
			if len(truncated) > 64 {
				truncated = truncated[:64]
			}
			slog.Debug("plan mode tool_result did not contain expected confirmation",
				"agent_id", a.AgentID(),
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

// claudeIsConsolidatable identifies native notices that can share one notification thread.
func claudeIsConsolidatable(content []byte) bool {
	return claudeProvider{}.Classify(content).Consolidatable()
}

var claudeSyntheticAPI5xxPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+5[0-9]{2}(?:$|[^[:alnum:]].*)`)
var claudeRetryableIdleTimeoutPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+Stream idle timeout(?:$|[^[:alnum:]].*)`)

// claudeRetryableOverloadedPattern matches the bare "Overloaded" result string.
// An overload is Anthropic HTTP 529 -- the most retryable error there is -- but
// Claude Code emits it in two forms: "API Error: 529 Overloaded" (with the
// numeric code, already caught by claudeSyntheticAPI5xxPattern) and a bare
// "API Error: Overloaded" with no code, which the 5xx pattern cannot match
// because it requires a three-digit code right after the punctuation. This
// pattern covers the code-less form so both spellings schedule a retry.
var claudeRetryableOverloadedPattern = regexp.MustCompile(`(?i)^API Error[^[:alnum:]]+Overloaded(?:$|[^[:alnum:]].*)`)

// isRetryableClaudeResultError reports whether a Claude result error should
// schedule an automatic retry. Matching is case-insensitive (each pattern carries the
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

// The states that a `system`/`session_state_changed` frame carries.
const (
	claudeSessionStateIdle           = "idle"
	claudeSessionStateRunning        = "running"
	claudeSessionStateRequiresAction = "requires_action"
)

// observeTurnFromOutput reads the CLI's turn state off one output frame and
// records both edges.
//
// It prefers the CLI's OWN answer. With claudeSessionStateEnv set, Claude Code
// publishes `system`/`session_state_changed` with running, requires_action or
// idle, and that frame is authoritative: `running` opens the turn at the top of
// the run loop, `requires_action` reports a turn that a permission prompt
// blocks (still in flight), and `idle` fires after the last result flushes and
// the background-agent loop exits. A ROOT frame that carries it therefore
// decides alone, and the heuristic below never sees it. A forwarded child's copy
// decides nothing, for the reason claudeIsLiveRootFrame gives.
//
// The heuristic below is for a build that publishes no such frame, which is
// every one that predates the variable. The FIRST session_state_changed frame
// retires it for the life of the process, so on a current CLI the turn flag
// moves only on session-state reports and the Worker's own SendInput.
// A result still completes its bookkeeping but does not release the turn.
func (a *Agent) observeTurnFromOutput(content []byte, msgType string) {
	if msgType == claudeMsgTypeSystem && claudeIsLiveRootFrame(content) {
		if state, ok := claudeSessionStateOf(content); ok {
			a.noteSessionState(state)
			return
		}
	}
	if a.publishesSessionState() {
		// This CLI states its turn state, and it did so already. Reading any
		// other frame as evidence could only disagree with it.
		return
	}
	a.armTurnFromOutput(content, msgType)
}

// claudeSessionStateOf reads the state of a `session_state_changed` frame, and
// reports false for every other frame.
//
// Claude Code publishes that `system` frame for its own turn state. It emits
// the frame only when claudeSessionStateEnv is set. Two paths read it, and the
// split is deliberate. observeTurnFromOutput reads the STATE it carries through
// this function, which is the whole reason this Worker asks for the frame.
// claudeHandleTaskEvent drops the frame from the timeline, because the turn
// flag already carries what it means and the CLI sends one at every edge of
// every turn.
//
// An unknown state value is REFUSED rather than treated as a turn. The three
// states are a closed set that the CLI's own schema states, so a fourth one is
// a build this Worker does not know -- and answering "a turn runs" for it would
// latch the queue on a frame nothing here can end.
func claudeSessionStateOf(content []byte) (string, bool) {
	var msg struct {
		Subtype string `json:"subtype"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(content, &msg); err != nil || msg.Subtype != contracts.ClaudeHiddenSystemSubtypeSessionStateChanged {
		return "", false
	}
	switch msg.State {
	case claudeSessionStateIdle, claudeSessionStateRunning, claudeSessionStateRequiresAction:
		return msg.State, true
	default:
		slog.Warn("unknown claude session state", "state", msg.State)
		return "", false
	}
}

// armTurnFromOutput detects a CLI-started turn when the build sends no session_state_changed frame.
// Native session state takes precedence. Its running state starts a turn, and its idle state ends the turn.
//
// This check runs before dispatch. Thinking telemetry returns early, and the root user tool_result never reaches persistence.
// Waiting for an assistant message would leave extended thinking outside the active turn.
// claudeIsLiveRootFrame rejects child frames. Results and control traffic cannot start a turn.
//
// The system subtypes have different owners:
//   - Thinking telemetry proves that the root model works.
//   - init follows running at each turn start. It supplies session data, not the turn signal.
//   - task_* and background_tasks_changed describe background work, which can continue after the root turn ends.
//   - Status and retry notices can describe housekeeping calls. A compaction boundary can arrive during resume before a turn starts.
//
// Only thinking telemetry starts a turn through this system-frame check.
// An unknown subtype starts nothing. A later assistant message can still prove that the turn runs.
// A false start would hold the input queue and the thinking indicator until the process exits.
// Without native session state, result ends the detected turn. With native session state, only idle ends it.
// Process exit releases a turn when the process sends no end.
func (a *Agent) armTurnFromOutput(content []byte, msgType string) {
	switch msgType {
	case claudeMsgTypeAssistant, claudeMsgTypeUser:
	case claudeMsgTypeSystem:
		if _, thinking := parseThinkingTokens(content); !thinking {
			return
		}
	default:
		return
	}
	if !claudeIsLiveRootFrame(content) {
		return
	}
	a.armTurn()
}

// claudeIsLiveRootFrame identifies live output from the root agent.
// Forwarded child frames and replayed records cannot change the root's live turn state.
// A child can continue or restart while its root is idle. Its parent_tool_use_id identifies that separate turn.
// Invalid JSON cannot establish a live root frame.
// Model changes can replay local command output before a resumed process emits its first session-state event.
// An absent replay flag means live output. A present flag must explicitly state false.
func claudeIsLiveRootFrame(content []byte) bool {
	var envelope struct {
		ParentToolUseID string          `json:"parent_tool_use_id"`
		IsReplay        json.RawMessage `json:"isReplay"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return false
	}
	return envelope.ParentToolUseID == "" &&
		(len(envelope.IsReplay) == 0 || bytes.Equal(bytes.TrimSpace(envelope.IsReplay), []byte("false")))
}

func (a *Agent) readOutputLoop(scanner *bufio.Scanner) {
	a.ReadOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
}

// handleOutput adapts the providerkit.ParsedLine to the existing HandleOutput method,
// passing the pre-parsed Type to avoid re-parsing the envelope.
func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	a.handleClaudeOutput(line.Raw, line.Type)
}
