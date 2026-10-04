package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type commandcodeProvider struct{ agent.ProviderDefaults }

func (p commandcodeProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	resolved, err := p.ProviderDefaults.ResolveResumeHandle(handle, homeDir)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(resolved, `/\\`) || resolved == "." || resolved == ".." {
		return "", fmt.Errorf("the Command Code resume handle must be a session token")
	}
	return resolved, nil
}

func (commandcodeProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Command Code", attachment)
}

func (commandcodeProvider) ListStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return storedSessions(ctx, query)
}

func (commandcodeProvider) IsInterrupt(content string) bool {
	var frame struct {
		Method string `json:"method"`
	}
	return json.Unmarshal([]byte(content), &frame) == nil && frame.Method == methodTurnInterrupt
}

func (commandcodeProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	event, err := eventFromFrame(raw)
	if err != nil {
		return agent.NotificationClassification{}
	}
	switch event.Type {
	case contracts.CommandCodeEventApiRetry:
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry}
	case contracts.CommandCodeEventCompactionDone:
		if event.Trigger != "manual" && event.TokensSaved > 0 {
			return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary}
		}
	case contracts.CommandCodeEventCompactionOutcome:
		if event.Trigger == "manual" && event.Outcome == "summarized" {
			return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary}
		}
	case contracts.CommandCodeEventNotice:
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus}
	}
	return agent.NotificationClassification{}
}

func (commandcodeProvider) TurnEndToolUses(content []byte) (int32, bool) {
	// The native boundary reports model turns. Worker metadata owns the tool count.
	return 0, false
}

func (commandcodeProvider) ExtractTodoEvent(_ string, content []byte, readRequest func() []byte) (todoevents.Event, bool) {
	event, err := eventFromFrame(content)
	if err != nil || event.Type != contracts.CommandCodeEventToolCompleted || readRequest == nil {
		return todoevents.Event{}, false
	}
	request, err := eventFromFrame(readRequest())
	if err != nil || request.ToolCallID == "" || request.ToolCallID != event.ToolCallID {
		return todoevents.Event{}, false
	}
	return todoEvent(request.ToolName, request.Input, nativeText(event.Result))
}
