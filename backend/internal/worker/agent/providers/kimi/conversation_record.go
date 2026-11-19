package kimi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
)

type kimiConversationNotice struct {
	Level  string  `json:"level"`
	Title  *string `json:"title"`
	Detail string  `json:"detail,omitempty"`
	Code   *string `json:"code,omitempty"`
}

type kimiConversationRecordView struct {
	record      contracts.KimiConversationRecord
	item        *kimiConversationItem
	turn        *kimiConversationTurnHeader
	notice      *kimiConversationNotice
	failure     *contracts.KimiConversationFailure
	turnContext *contracts.KimiConversationTurnContext
	retry       *kimiConversationRetry
}

func encodeKimiConversationRecord(record contracts.KimiConversationRecord) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode the Kimi conversation record: %w", err)
	}
	if _, err := decodeKimiConversationRecord(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeKimiConversationRecord(raw []byte) (kimiConversationRecordView, error) {
	var view kimiConversationRecordView
	if err := json.Unmarshal(raw, &view.record); err != nil {
		return view, fmt.Errorf("decode the Kimi conversation record: %w", err)
	}
	record := &view.record
	if record.SessionID == "" || record.AgentID == "" || record.EventCreatedAt != "" && !kimiConversationTimeValid(record.EventCreatedAt) {
		return view, fmt.Errorf("the Kimi conversation record has invalid ownership fields")
	}
	if record.OutputTruncated && (record.Type != contracts.KimiConversationRecordKindItem || record.Role != contracts.KimiConversationRecordRoleResult) {
		return view, fmt.Errorf("the Kimi output truncation flag requires a tool result")
	}
	switch record.Type {
	case contracts.KimiConversationRecordKindFailure:
		if record.TurnID != "" || record.Role != "" || len(record.Item) != 0 || len(record.Turn) != 0 || len(record.Notice) != 0 || len(record.Failure) == 0 || record.EventCreatedAt == "" {
			return view, fmt.Errorf("the Kimi conversation failure has invalid record ownership")
		}
		if err := json.Unmarshal(record.Failure, &view.failure); err != nil || view.failure == nil || view.failure.Type != contracts.KimiFrameTurnsFailed ||
			view.failure.SessionID != record.SessionID || view.failure.AgentID != record.AgentID || view.failure.EventCreatedAt != record.EventCreatedAt ||
			view.failure.Code == nil || view.failure.Msg == nil {
			return view, fmt.Errorf("the Kimi conversation failure packet has another native owner or invalid diagnostic fields")
		}
	case contracts.KimiConversationRecordKindRetry:
		if record.TurnID == "" || record.StepIndex <= 0 || len(record.Retry) == 0 || record.Role != "" || len(record.Item) != 0 || len(record.Turn) != 0 || len(record.Notice) != 0 {
			return view, fmt.Errorf("the Kimi retry record has no complete native request owner")
		}
		if err := json.Unmarshal(record.Retry, &view.retry); err != nil || view.retry == nil || !view.retry.valid() {
			return view, fmt.Errorf("the Kimi retry record has invalid native retry state")
		}
	case contracts.KimiConversationRecordKindItem:
		if record.TurnID == "" || len(record.Item) == 0 || len(record.Turn) != 0 || len(record.Notice) != 0 {
			return view, fmt.Errorf("the Kimi item record has no complete identity or item")
		}
		if err := json.Unmarshal(record.Item, &view.item); err != nil || view.item == nil {
			return view, fmt.Errorf("the Kimi item record has an invalid item")
		}
		if err := view.item.validate(); err != nil {
			return view, err
		}
		if record.OutputTruncated && (view.item.Type != contracts.KimiConversationItemKindToolCall || len(view.item.Output) == 0) {
			return view, fmt.Errorf("the Kimi output truncation flag requires retained output")
		}
		if view.item.Type == contracts.KimiConversationItemKindToolCall {
			if record.Role != contracts.KimiConversationRecordRoleRequest && record.Role != contracts.KimiConversationRecordRoleResult {
				return view, fmt.Errorf("the Kimi tool record has no request or result role")
			}
		} else if record.Role == contracts.KimiConversationRecordRoleOrigin {
			if record.AgentID != kimiMainAgentID || !view.item.hasOriginNotice() {
				return view, fmt.Errorf("the Kimi origin notice does not belong to an identified main-agent opener")
			}
		} else if record.Role != contracts.KimiConversationRecordRoleItem {
			return view, fmt.Errorf("the Kimi conversation item has an invalid record role")
		}
	case contracts.KimiConversationRecordKindTurn:
		if record.TurnID == "" || len(record.Item) != 0 || len(record.Turn) == 0 || len(record.Notice) != 0 || record.Role != "" {
			return view, fmt.Errorf("the Kimi turn record has no complete identity or header")
		}
		if err := json.Unmarshal(record.Turn, &view.turn); err != nil || view.turn == nil {
			return view, fmt.Errorf("the Kimi turn record has an invalid header")
		}
		if err := view.turn.validate(); err != nil {
			return view, err
		}
		if view.turn.TurnID != record.TurnID {
			return view, fmt.Errorf("the Kimi turn record header belongs to another turn")
		}
	case contracts.KimiConversationRecordKindNotice:
		if record.TurnID != "" || record.Role != "" || len(record.Item) != 0 || len(record.Turn) != 0 || len(record.Notice) == 0 || record.EventCreatedAt == "" {
			return view, fmt.Errorf("the Kimi notice record has invalid identity or notice fields")
		}
		if err := json.Unmarshal(record.Notice, &view.notice); err != nil || view.notice == nil || view.notice.Title == nil ||
			view.notice.Level != contracts.KimiNoticeLevelInfo && view.notice.Level != contracts.KimiNoticeLevelWarning && view.notice.Level != contracts.KimiNoticeLevelError {
			return view, fmt.Errorf("the Kimi notice record has an invalid notice")
		}
	default:
		return view, fmt.Errorf("the Kimi conversation record has an unknown type")
	}
	if record.Type != contracts.KimiConversationRecordKindFailure && len(record.Failure) != 0 {
		return view, fmt.Errorf("the Kimi conversation failure packet belongs to another record kind")
	}
	if record.Type != contracts.KimiConversationRecordKindRetry && (record.StepIndex != 0 || len(record.Retry) != 0) {
		return view, fmt.Errorf("the Kimi retry fields belong to another record kind")
	}
	if len(record.TurnContext) != 0 {
		if record.Type != contracts.KimiConversationRecordKindItem || record.Role == contracts.KimiConversationRecordRoleRequest {
			return view, fmt.Errorf("the Kimi outcome context belongs to another record role")
		}
		if err := json.Unmarshal(record.TurnContext, &view.turnContext); err != nil || view.turnContext == nil ||
			view.turnContext.TurnID != record.TurnID ||
			view.turnContext.Kind != contracts.KimiConversationTurnKindAgent && view.turnContext.Kind != contracts.KimiConversationTurnKindSystem ||
			view.turnContext.Status != contracts.KimiConversationStatusRunning && view.turnContext.Status != contracts.KimiConversationStatusEnded ||
			view.turnContext.Outcome != "" && !kimiConversationOutcomeValid(view.turnContext.Outcome) {
			return view, fmt.Errorf("the Kimi item outcome context has invalid native ownership or state")
		}
	}
	return view, nil
}

// key excludes mutable data. A later native view therefore resolves the same
// stored record without replacing its original bytes or completion effects.
func (view kimiConversationRecordView) key() string {
	identity := []string{view.record.Type, view.record.SessionID, view.record.AgentID, view.record.TurnID, view.record.Role}
	if view.item != nil {
		identity = append(identity, view.item.ItemID)
	}
	if view.retry != nil && view.record.StepIndex != 0 {
		identity = append(identity, strconv.FormatInt(view.record.StepIndex, 10), strconv.FormatInt(view.retry.NextAttempt, 10))
	}
	if view.notice != nil {
		identity = append(identity, view.record.EventCreatedAt, string(view.record.Notice))
	}
	if view.failure != nil {
		identity = append(identity, view.record.EventCreatedAt, string(view.record.Failure))
	}
	raw, _ := json.Marshal(identity)
	digest := sha256.Sum256(raw)
	return "kimi:conversation:" + hex.EncodeToString(digest[:])
}

// acceptsView preserves native ownership facts learned by an earlier view.
func (view kimiConversationRecordView) acceptsView(other kimiConversationRecordView) bool {
	if view.record.Type != other.record.Type || view.record.SessionID != other.record.SessionID || view.record.AgentID != other.record.AgentID ||
		view.record.TurnID != other.record.TurnID || view.record.Role != other.record.Role {
		return false
	}
	if view.failure != nil || other.failure != nil {
		return view.failure != nil && other.failure != nil && view.key() == other.key()
	}
	if view.retry != nil || other.retry != nil {
		return view.retry != nil && other.retry != nil && view.record.StepIndex != 0 && other.record.StepIndex != 0 &&
			view.record.StepIndex == other.record.StepIndex && view.retry.NextAttempt == other.retry.NextAttempt
	}
	if view.item != nil || other.item != nil {
		return view.item != nil && other.item != nil && view.item.ItemID == other.item.ItemID && view.item.Type == other.item.Type &&
			(view.item.StepIndex == nil || other.item.StepIndex == nil || *view.item.StepIndex == *other.item.StepIndex) &&
			(view.turnContext == nil || other.turnContext != nil && view.turnContext.Kind == other.turnContext.Kind)
	}
	if view.turn != nil || other.turn != nil {
		return view.turn != nil && other.turn != nil && sameKimiConversationTurnIdentity(*view.turn, *other.turn)
	}
	return view.notice == nil && other.notice == nil || view.notice != nil && other.notice != nil && view.key() == other.key()
}
