package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type museProvider struct{ agent.ProviderDefaults }

func (museProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Muse Code MSP", attachment)
}
func (museProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}
}
func (museProvider) IsInterrupt(text string) bool {
	var message frame
	return json.Unmarshal([]byte(text), &message) == nil && message.Method == methodTurnInterrupt
}
func (p museProvider) ResolveResumeHandle(handle, home string) (string, error) {
	id, err := p.ProviderDefaults.ResolveResumeHandle(handle, home)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("the Muse resume handle must be a session token")
	}
	return id, nil
}
func (museProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return storedSessions(ctx, q)
}
func (museProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var message struct {
		Method string     `json:"method"`
		Params itemParams `json:"params"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return agent.NotificationClassification{}
	}
	if message.Method == contracts.MuseMethodTurnRetryScheduled {
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry}
	}
	if message.Method == contracts.MuseMethodItemCompleted && message.Params.Item.Kind == contracts.MuseItemKindCompaction && message.Params.Item.Outcome == contracts.MuseCompactionOutcomeCompacted {
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary}
	}
	return agent.NotificationClassification{}
}
func (museProvider) ExtractTodoEvent(_ string, raw []byte, _ func() []byte) (todoevents.Event, bool) {
	var message struct {
		Method string `json:"method"`
		Params struct {
			Items *[]struct {
				Text       *string         `json:"text"`
				Status     *string         `json:"status"`
				ActiveForm json.RawMessage `json:"activeForm"`
			} `json:"items"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Method != contracts.MuseMethodTodoListChanged || message.Params.Items == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*message.Params.Items))
	for _, row := range *message.Params.Items {
		if row.Text == nil || strings.TrimSpace(*row.Text) == "" || row.Status == nil {
			return todoevents.Event{}, false
		}
		var status todoevents.Status
		switch *row.Status {
		case contracts.MuseTodoStatusPending:
			status = todoevents.StatusPending
		case contracts.MuseTodoStatusInProgress:
			status = todoevents.StatusInProgress
		case contracts.MuseTodoStatusCompleted:
			status = todoevents.StatusCompleted
		case contracts.MuseTodoStatusCancelled:
			status = todoevents.StatusDeleted
		default:
			return todoevents.Event{}, false
		}
		var activeForm string
		if len(row.ActiveForm) > 0 && (bytes.Equal(bytes.TrimSpace(row.ActiveForm), []byte("null")) || json.Unmarshal(row.ActiveForm, &activeForm) != nil) {
			return todoevents.Event{}, false
		}
		items = append(items, todoevents.Item{Content: *row.Text, Status: status, ActiveForm: activeForm})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
