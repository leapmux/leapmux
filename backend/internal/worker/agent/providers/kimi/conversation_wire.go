package kimi

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

// kimiConversationFrame carries the public turn and item feed of kap-server.
// A subscription sends its latest turn page before its subsequent item changes.
type kimiConversationFrame struct {
	Type           string                      `json:"type"`
	SessionID      string                      `json:"session_id"`
	AgentID        string                      `json:"agent_id"`
	EventCreatedAt string                      `json:"event_created_at"`
	Turns          []kimiConversationTurn      `json:"turns"`
	HasMore        *bool                       `json:"has_more"`
	Turn           *kimiConversationTurnHeader `json:"turn"`
	TurnID         string                      `json:"turn_id"`
	TurnIDs        []string                    `json:"turn_ids"`
	Step           *kimiConversationStep       `json:"step"`
	Retry          *kimiConversationRetry      `json:"retry"`
	Item           *kimiConversationItem       `json:"item"`
	ItemID         string                      `json:"item_id"`
	Set            map[string]json.RawMessage  `json:"set"`
	Field          string                      `json:"field"`
	Delta          *string                     `json:"delta"`
	Code           json.RawMessage             `json:"code"`
	Message        *string                     `json:"msg"`
	Level          string                      `json:"level"`
	Title          *string                     `json:"title"`
	Detail         string                      `json:"detail"`
}

type kimiConversationTurnHeader struct {
	TurnID     string                 `json:"turn_id"`
	Ordinal    *int64                 `json:"ordinal"`
	Kind       string                 `json:"kind"`
	Status     string                 `json:"status"`
	Outcome    string                 `json:"outcome,omitempty"`
	EndReason  string                 `json:"end_reason,omitempty"`
	Error      *kimiConversationError `json:"error,omitempty"`
	Retry      *kimiConversationRetry `json:"retry,omitempty"`
	TurnNumber *int64                 `json:"turn_number,omitempty"`
	StartedAt  string                 `json:"started_at"`
	EndedAt    string                 `json:"ended_at,omitempty"`
	DurationMS *float64               `json:"duration_ms,omitempty"`
	Usage      *kimiConversationUsage `json:"usage,omitempty"`
	Steps      []kimiConversationStep `json:"steps"`
}

type kimiConversationTurn struct {
	kimiConversationTurnHeader
	Items []kimiConversationItem `json:"items"`
}

type kimiConversationStep struct {
	Index        int64                  `json:"index"`
	Outcome      string                 `json:"outcome"`
	StartedAt    string                 `json:"started_at"`
	EndedAt      string                 `json:"ended_at,omitempty"`
	FinishReason string                 `json:"finish_reason,omitempty"`
	Usage        *kimiConversationUsage `json:"usage,omitempty"`
}

type kimiConversationUsage struct {
	InputOther         *float64 `json:"input_other"`
	Output             *float64 `json:"output"`
	InputCacheRead     *float64 `json:"input_cache_read"`
	InputCacheCreation *float64 `json:"input_cache_creation"`
}

type kimiConversationError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Name       string `json:"name,omitempty"`
	StatusCode *int   `json:"status_code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Retryable  *bool  `json:"retryable,omitempty"`
}

type kimiConversationRetry struct {
	Attempt     int64    `json:"attempt"`
	NextAttempt int64    `json:"next_attempt"`
	MaxAttempts int64    `json:"max_attempts"`
	DelayMS     *float64 `json:"delay_ms"`
	Error       *struct {
		Name       *string `json:"name"`
		Message    *string `json:"message"`
		StatusCode *int    `json:"status_code,omitempty"`
	} `json:"error"`
}

// kimiConversationItem keeps native fields without an obsolete frame or step ID.
// The session, agent, turn_id and item_id identify one item; step_index is metadata.
type kimiConversationItem struct {
	Type             string                     `json:"type"`
	ItemID           string                     `json:"item_id"`
	StepIndex        *int64                     `json:"step_index,omitempty"`
	Status           string                     `json:"status"`
	Outcome          string                     `json:"outcome,omitempty"`
	CreatedAt        string                     `json:"created_at"`
	StartedAt        string                     `json:"started_at,omitempty"`
	EndedAt          string                     `json:"ended_at,omitempty"`
	DurationMS       *int64                     `json:"duration_ms,omitempty"`
	Text             *string                    `json:"text,omitempty"`
	PromptIDs        []string                   `json:"prompt_ids"`
	Placement        string                     `json:"placement,omitempty"`
	Origin           json.RawMessage            `json:"origin,omitempty"`
	Attachments      []json.RawMessage          `json:"attachments"`
	Skills           []json.RawMessage          `json:"skills"`
	Metadata         json.RawMessage            `json:"metadata,omitempty"`
	Name             *string                    `json:"name,omitempty"`
	Stage            string                     `json:"stage,omitempty"`
	Input            json.RawMessage            `json:"input,omitempty"`
	InputText        *string                    `json:"input_text,omitempty"`
	Display          json.RawMessage            `json:"display,omitempty"`
	Output           json.RawMessage            `json:"output,omitempty"`
	Media            []kimiConversationMedia    `json:"media,omitempty"`
	Error            *string                    `json:"error,omitempty"`
	Note             *string                    `json:"note,omitempty"`
	Fields           map[string]json.RawMessage `json:"fields,omitempty"`
	TaskID           string                     `json:"task_id,omitempty"`
	InteractionID    string                     `json:"interaction_id,omitempty"`
	AgentRefs        []kimiConversationAgentRef `json:"agent_refs,omitempty"`
	NotificationID   *string                    `json:"notification_id,omitempty"`
	Title            *string                    `json:"title,omitempty"`
	Body             *string                    `json:"body,omitempty"`
	Severity         string                     `json:"severity,omitempty"`
	NotificationType *string                    `json:"notification_type,omitempty"`
	Source           json.RawMessage            `json:"source,omitempty"`
	AgentID          string                     `json:"agent_id,omitempty"`
	Answer           json.RawMessage            `json:"answer,omitempty"`
	SystemKind       string                     `json:"kind,omitempty"`
	Trigger          string                     `json:"trigger,omitempty"`
	Instruction      *string                    `json:"instruction,omitempty"`
	TokensBefore     *float64                   `json:"tokens_before,omitempty"`
	TokensAfter      *float64                   `json:"tokens_after,omitempty"`
	CompactedCount   *int64                     `json:"compacted_count,omitempty"`
	Summary          *string                    `json:"summary,omitempty"`
	ForkedFrom       *struct {
		AgentID string `json:"agent_id"`
	} `json:"forked_from,omitempty"`
	Debug json.RawMessage `json:"debug,omitempty"`
}

type kimiConversationMedia struct {
	Kind      string `json:"kind"`
	FileID    string `json:"file_id,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type kimiConversationAgentRef struct {
	AgentID    string `json:"agent_id"`
	Role       string `json:"role"`
	SwarmIndex *int64 `json:"swarm_index,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
}

type kimiConversationOrigin struct {
	Kind           string  `json:"kind"`
	Name           *string `json:"name,omitempty"`
	Path           *string `json:"path,omitempty"`
	Args           *string `json:"args,omitempty"`
	PluginID       *string `json:"plugin_id,omitempty"`
	Command        *string `json:"command,omitempty"`
	JobID          *string `json:"job_id,omitempty"`
	Schedule       *string `json:"schedule,omitempty"`
	Recurring      *bool   `json:"recurring,omitempty"`
	CoalescedCount *int64  `json:"coalesced_count,omitempty"`
	Stale          *bool   `json:"stale,omitempty"`
	HookEvent      *string `json:"hook_event,omitempty"`
}

func decodeKimiConversationOrigin(raw []byte) (kimiConversationOrigin, error) {
	var origin kimiConversationOrigin
	if err := json.Unmarshal(raw, &origin); err != nil {
		return origin, fmt.Errorf("decode the Kimi conversation origin: %w", err)
	}
	valid := false
	switch origin.Kind {
	case contracts.KimiConversationOriginUser, contracts.KimiConversationOriginGoal,
		contracts.KimiConversationOriginRetry, contracts.KimiConversationOriginSystem:
		valid = true
	case contracts.KimiConversationOriginSkill:
		valid = origin.Name != nil
	case contracts.KimiConversationOriginPluginCommand:
		valid = origin.PluginID != nil && origin.Command != nil
	case contracts.KimiConversationOriginCron:
		valid = origin.JobID != nil && origin.Schedule != nil && origin.Recurring != nil && origin.CoalescedCount != nil && *origin.CoalescedCount >= 0 && origin.Stale != nil
	case contracts.KimiConversationOriginHook:
		valid = origin.HookEvent != nil
	}
	if !valid {
		return origin, fmt.Errorf("the Kimi conversation origin has invalid fields")
	}
	return origin, nil
}

func (item kimiConversationItem) hasOriginNotice() bool {
	if item.Type != contracts.KimiConversationItemKindUserMessage || item.Status != contracts.KimiConversationStatusEnded || item.Placement != "opener" {
		return false
	}
	origin, err := decodeKimiConversationOrigin(item.Origin)
	return err == nil && origin.Kind != contracts.KimiConversationOriginUser && origin.Kind != contracts.KimiConversationOriginSystem
}

type kimiConversationAttachment struct {
	Kind      string  `json:"kind"`
	Ordinal   *int64  `json:"ordinal"`
	FileID    *string `json:"file_id"`
	URL       *string `json:"url"`
	Name      *string `json:"name"`
	MediaType *string `json:"media_type"`
	Size      *int64  `json:"size"`
}

type kimiConversationSkill struct {
	Name *string `json:"name"`
	Path *string `json:"path"`
	Args *string `json:"args"`
}

type kimiConversationNotificationSource struct {
	Kind *string `json:"kind"`
	ID   *string `json:"id"`
}

type kimiConversationNotificationOutput struct {
	File      *string `json:"file"`
	Preview   *string `json:"preview"`
	Bytes     *int64  `json:"bytes"`
	Truncated *bool   `json:"truncated"`
}

type kimiConversationNotificationAnswer struct {
	Answers map[string]json.RawMessage `json:"answers"`
	Note    *string                    `json:"note"`
}

func validateKimiConversationAttachments(values []json.RawMessage) error {
	for _, raw := range values {
		var attachment *kimiConversationAttachment
		if json.Unmarshal(raw, &attachment) != nil || attachment == nil || attachment.Ordinal == nil || *attachment.Ordinal <= 0 ||
			attachment.Kind != "image" && attachment.Kind != "video" && attachment.Kind != "audio" && attachment.Kind != "file" || attachment.Size != nil && *attachment.Size < 0 {
			return fmt.Errorf("the Kimi user message has an invalid attachment")
		}
	}
	return nil
}

func (item kimiConversationItem) validateUserFields() error {
	if _, err := decodeKimiConversationOrigin(item.Origin); err != nil {
		return err
	}
	if err := validateKimiConversationAttachments(item.Attachments); err != nil {
		return err
	}
	for _, raw := range item.Skills {
		var skill *kimiConversationSkill
		if json.Unmarshal(raw, &skill) != nil || skill == nil || skill.Name == nil {
			return fmt.Errorf("the Kimi user message has an invalid skill")
		}
	}
	if len(item.Metadata) > 0 {
		var metadata map[string]json.RawMessage
		if json.Unmarshal(item.Metadata, &metadata) != nil || metadata == nil {
			return fmt.Errorf("the Kimi user message has invalid metadata")
		}
	}
	return nil
}

func (item kimiConversationItem) validateNotificationFields() error {
	var source *kimiConversationNotificationSource
	if json.Unmarshal(item.Source, &source) != nil || source == nil || source.Kind == nil || source.ID == nil {
		return fmt.Errorf("the Kimi conversation notification has an invalid source")
	}
	if len(item.Output) > 0 {
		var output *kimiConversationNotificationOutput
		if json.Unmarshal(item.Output, &output) != nil || output == nil || output.Bytes != nil && *output.Bytes < 0 {
			return fmt.Errorf("the Kimi conversation notification has invalid output metadata")
		}
	}
	if len(item.Answer) > 0 {
		var answer *kimiConversationNotificationAnswer
		if json.Unmarshal(item.Answer, &answer) != nil || answer == nil || answer.Answers == nil {
			return fmt.Errorf("the Kimi conversation notification has invalid answer metadata")
		}
		for _, raw := range answer.Answers {
			if string(raw) == "true" {
				continue
			}
			var text *string
			if json.Unmarshal(raw, &text) != nil || text == nil {
				return fmt.Errorf("the Kimi conversation notification has an invalid answer value")
			}
		}
	}
	return nil
}

func isKimiConversationFrame(kind string) bool {
	switch kind {
	case contracts.KimiFrameTurns, contracts.KimiFrameTurnsRemoved, contracts.KimiFrameTurnsFailed,
		contracts.KimiFrameTurnStarted, contracts.KimiFrameTurnEnded, contracts.KimiFrameTurnUpdated,
		contracts.KimiFrameItemStarted, contracts.KimiFrameItemEnded, contracts.KimiFrameItemUpdated,
		contracts.KimiFrameItemDelta, contracts.KimiFrameItemRemoved, contracts.KimiFrameNotice:
		return true
	default:
		return false
	}
}

func decodeKimiConversationFrame(raw []byte) (kimiConversationFrame, error) {
	var frame kimiConversationFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return frame, fmt.Errorf("decode the Kimi conversation frame: %w", err)
	}
	if !isKimiConversationFrame(frame.Type) || frame.SessionID == "" || frame.AgentID == "" || !kimiConversationTimeValid(frame.EventCreatedAt) {
		return frame, fmt.Errorf("the Kimi conversation frame has invalid identity or event fields")
	}
	switch frame.Type {
	case contracts.KimiFrameTurns:
		if err := validateKimiConversationPage(frame.Turns, frame.HasMore); err != nil {
			return frame, err
		}
	case contracts.KimiFrameTurnsRemoved:
		if frame.TurnIDs == nil || slices.Contains(frame.TurnIDs, "") {
			return frame, fmt.Errorf("the Kimi conversation removal has no complete turn list")
		}
	case contracts.KimiFrameTurnsFailed:
		var code *int
		if json.Unmarshal(frame.Code, &code) != nil || code == nil || frame.Message == nil {
			return frame, fmt.Errorf("the Kimi conversation failure has no result code or message")
		}
	case contracts.KimiFrameTurnStarted, contracts.KimiFrameTurnEnded:
		if frame.Turn == nil {
			return frame, fmt.Errorf("the Kimi conversation event has no turn header")
		}
		if err := frame.Turn.validate(); err != nil {
			return frame, err
		}
		if frame.Type == contracts.KimiFrameTurnEnded && frame.Turn.Status != "ended" {
			return frame, fmt.Errorf("the Kimi turn completion still reports a running turn")
		}
	case contracts.KimiFrameTurnUpdated:
		if frame.TurnID == "" || frame.Step != nil && !frame.Step.valid() || !frame.Retry.valid() {
			return frame, fmt.Errorf("the Kimi turn update has invalid fields")
		}
	case contracts.KimiFrameItemStarted, contracts.KimiFrameItemEnded:
		if frame.TurnID == "" || frame.Item == nil {
			return frame, fmt.Errorf("the Kimi conversation event has no turn or item identity")
		}
		if err := frame.Item.validate(); err != nil {
			return frame, err
		}
		if frame.Type == contracts.KimiFrameItemEnded && frame.Item.Status != "ended" {
			return frame, fmt.Errorf("the Kimi item completion still reports a running item")
		}
	case contracts.KimiFrameItemUpdated:
		if frame.TurnID == "" || frame.ItemID == "" || frame.Set == nil {
			return frame, fmt.Errorf("the Kimi item update has no identity or field changes")
		}
		for _, field := range []string{"type", "item_id"} {
			if _, changed := frame.Set[field]; changed {
				return frame, fmt.Errorf("the Kimi item update changes its native identity")
			}
		}
	case contracts.KimiFrameItemDelta:
		if frame.TurnID == "" || frame.ItemID == "" || frame.Delta == nil || frame.Field != "text" && frame.Field != "input_text" && frame.Field != "output" {
			return frame, fmt.Errorf("the Kimi item delta has invalid identity or text fields")
		}
	case contracts.KimiFrameItemRemoved:
		if frame.TurnID == "" || frame.ItemID == "" {
			return frame, fmt.Errorf("the Kimi item removal has no complete identity")
		}
	case contracts.KimiFrameNotice:
		if frame.Level != "info" && frame.Level != "warning" && frame.Level != "error" || frame.Title == nil {
			return frame, fmt.Errorf("the Kimi conversation notice has invalid fields")
		}
		if len(frame.Code) > 0 {
			var code *string
			if json.Unmarshal(frame.Code, &code) != nil || code == nil {
				return frame, fmt.Errorf("the Kimi conversation notice has an invalid code")
			}
		}
	}
	return frame, nil
}

func kimiConversationTimeValid(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func kimiConversationOutcomeValid(value string) bool {
	return value == "completed" || value == "interrupted" || value == "failed"
}

func (usage *kimiConversationUsage) valid() bool {
	if usage == nil {
		return true
	}
	for _, value := range []*float64{usage.InputOther, usage.Output, usage.InputCacheRead, usage.InputCacheCreation} {
		if value == nil || *value < 0 {
			return false
		}
	}
	return true
}

func (retry *kimiConversationRetry) valid() bool {
	return retry == nil || retry.Attempt > 0 && retry.NextAttempt > 0 && retry.MaxAttempts > 0 && retry.DelayMS != nil && *retry.DelayMS >= 0 &&
		retry.Error != nil && retry.Error.Name != nil && retry.Error.Message != nil
}

func (step kimiConversationStep) valid() bool {
	return step.Index > 0 && kimiConversationOutcomeValid(step.Outcome) && kimiConversationTimeValid(step.StartedAt) &&
		(step.EndedAt == "" || kimiConversationTimeValid(step.EndedAt)) && step.Usage.valid()
}

func (turn kimiConversationTurnHeader) validate() error {
	if !kimiConversationEndReasonValid(turn.EndReason) {
		return fmt.Errorf("the Kimi conversation turn has an unknown end reason")
	}
	if turn.TurnID == "" || turn.Ordinal == nil || *turn.Ordinal < 0 || turn.Kind != "agent" && turn.Kind != "system" ||
		turn.Status != "running" && turn.Status != "ended" || turn.Outcome != "" && !kimiConversationOutcomeValid(turn.Outcome) ||
		!kimiConversationTimeValid(turn.StartedAt) || turn.EndedAt != "" && !kimiConversationTimeValid(turn.EndedAt) ||
		turn.TurnNumber != nil && *turn.TurnNumber < 0 || turn.DurationMS != nil && *turn.DurationMS < 0 || turn.Steps == nil ||
		!turn.Usage.valid() || !turn.Retry.valid() {
		return fmt.Errorf("the Kimi conversation turn has invalid fields")
	}
	indices := make(map[int64]struct{}, len(turn.Steps))
	for _, step := range turn.Steps {
		if _, duplicate := indices[step.Index]; duplicate || !step.valid() {
			return fmt.Errorf("the Kimi conversation turn has an invalid or repeated step")
		}
		indices[step.Index] = struct{}{}
	}
	return nil
}

func kimiConversationEndReasonValid(reason string) bool {
	switch reason {
	case "", contracts.KimiConversationEndReasonUserCancelled, contracts.KimiConversationEndReasonAborted,
		contracts.KimiConversationEndReasonMaxSteps, contracts.KimiConversationEndReasonError,
		contracts.KimiConversationEndReasonFiltered, contracts.KimiConversationEndReasonBlocked:
		return true
	default:
		return false
	}
}

func (turn kimiConversationTurn) validate() error {
	if err := turn.kimiConversationTurnHeader.validate(); err != nil {
		return err
	}
	if turn.Items == nil {
		return fmt.Errorf("the Kimi conversation page has no item list for a turn")
	}
	identities := make(map[string]struct{}, len(turn.Items))
	for _, item := range turn.Items {
		if _, duplicate := identities[item.ItemID]; duplicate {
			return fmt.Errorf("the Kimi conversation turn repeats an item identity")
		}
		if err := item.validate(); err != nil {
			return err
		}
		identities[item.ItemID] = struct{}{}
	}
	return nil
}

func (item kimiConversationItem) validate() error {
	if item.ItemID == "" || item.Status != "running" && item.Status != "ended" ||
		item.Outcome != "" && !kimiConversationOutcomeValid(item.Outcome) || !kimiConversationTimeValid(item.CreatedAt) ||
		item.StepIndex != nil && *item.StepIndex <= 0 || item.DurationMS != nil && *item.DurationMS < 0 ||
		item.StartedAt != "" && !kimiConversationTimeValid(item.StartedAt) || item.EndedAt != "" && !kimiConversationTimeValid(item.EndedAt) {
		return fmt.Errorf("the Kimi conversation item has invalid fields")
	}
	switch item.Type {
	case "user_message":
		if item.Text == nil || item.PromptIDs == nil || slices.Contains(item.PromptIDs, "") ||
			item.Placement != "opener" && item.Placement != "steer" || len(item.Origin) == 0 || item.Attachments == nil || item.Skills == nil {
			return fmt.Errorf("the Kimi user message has invalid fields")
		}
		if err := item.validateUserFields(); err != nil {
			return err
		}
	case "assistant_message", "reasoning":
		if item.Text == nil || item.Type == "reasoning" && item.StartedAt == "" {
			return fmt.Errorf("the Kimi conversation text has invalid fields")
		}
	case "tool_call":
		if item.Name == nil || item.StartedAt == "" || item.Stage != "" && item.Stage != "input" && item.Stage != "awaiting_approval" && item.Stage != "executing" {
			return fmt.Errorf("the Kimi conversation tool has invalid fields")
		}
		if len(item.Output) > 0 {
			var text string
			if string(item.Output) == "null" || json.Unmarshal(item.Output, &text) != nil {
				return fmt.Errorf("the Kimi conversation tool output is not text")
			}
		}
		for _, ref := range item.AgentRefs {
			if ref.AgentID == "" || ref.Role != "child" && ref.Role != "member" || ref.SwarmIndex != nil && *ref.SwarmIndex < 0 {
				return fmt.Errorf("the Kimi conversation tool has an invalid agent reference")
			}
		}
		for _, media := range item.Media {
			if media.Kind != "image" && media.Kind != "video" && media.Kind != "audio" {
				return fmt.Errorf("the Kimi conversation tool has an invalid media kind")
			}
		}
	case "notification":
		if item.NotificationID == nil || item.Title == nil || item.Body == nil || item.NotificationType == nil ||
			item.Severity != "info" && item.Severity != "warning" && item.Severity != "error" || len(item.Source) == 0 {
			return fmt.Errorf("the Kimi conversation notification has invalid fields")
		}
		if err := item.validateNotificationFields(); err != nil {
			return err
		}
	case "system":
		switch item.SystemKind {
		case contracts.KimiConversationSystemKindCompaction:
			if item.Trigger != "manual" && item.Trigger != "auto" || item.TokensBefore != nil && *item.TokensBefore < 0 ||
				item.TokensAfter != nil && *item.TokensAfter < 0 || item.CompactedCount != nil && *item.CompactedCount < 0 {
				return fmt.Errorf("the Kimi compaction item has invalid fields")
			}
		case contracts.KimiConversationSystemKindForkBoundary:
			var source string
			if json.Unmarshal(item.Source, &source) != nil || source != "btw" && source != "fork" || item.ForkedFrom == nil || item.ForkedFrom.AgentID == "" {
				return fmt.Errorf("the Kimi fork boundary has invalid fields")
			}
		default:
			return fmt.Errorf("the Kimi conversation system item has an unknown kind")
		}
	default:
		return fmt.Errorf("the Kimi conversation item has an unknown type")
	}
	return nil
}
