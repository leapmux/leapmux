package deepseekharness

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type deepseekHarnessProvider struct{ agent.ProviderDefaults }

func (deepseekHarnessProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	var original, extra map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil || json.Unmarshal(content.Supplemental, &extra) != nil {
		return content.Original
	}
	if index := extra[contracts.DeepseekHarnessSupplementBlockIndex]; len(index) > 0 {
		var value *int
		if json.Unmarshal(index, &value) == nil && value != nil && *value >= 0 {
			original[contracts.DeepseekHarnessSupplementBlockIndex] = index
		}
	}
	raw, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return raw
}

func (deepseekHarnessProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var event sessionEvent
	if json.Unmarshal(raw, &event) != nil {
		return agent.NotificationClassification{}
	}
	if event.Type == contracts.DeepseekHarnessEventCompactionEnd {
		var data struct {
			Error *string `json:"error"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			return agent.NotificationClassification{}
		}
		if data.Error == nil {
			return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "deepseek-harness:compaction"}
		}
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "deepseek-harness:compaction"}
	}
	return agent.NotificationClassification{}
}

func (deepseekHarnessProvider) PlanModeControl(name string) agent.PlanModeControlKind {
	if name == contracts.DeepseekHarnessToolExitPlanMode {
		return agent.PlanModeControlExit
	}
	return agent.PlanModeControlNone
}

func (deepseekHarnessProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlEnter {
		return contracts.DeepseekHarnessModePlan
	}
	if kind == agent.PlanModeControlExit {
		return contracts.DeepseekHarnessModeAct
	}
	return ""
}

func (deepseekHarnessProvider) PlanApprovalOptions(permissionMode string) map[string]string {
	if permissionMode == "" {
		permissionMode = contracts.DeepseekHarnessModeAct
	}
	return map[string]string{agent.OptionIDPermissionMode: permissionMode}
}

func (deepseekHarnessProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	resolution := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) > 0 {
		if refusal := storedControlRefusal(ctx); refusal != "" {
			resolution.Refuse(refusal)
			return resolution
		}
	}
	if ctx.ToolName == contracts.DeepseekHarnessToolExitPlanMode {
		resolution.PlanModeControl = agent.PlanModeControlExit
	}
	return resolution
}

func (deepseekHarnessProvider) TurnEndToolUses(raw []byte) (int32, bool) {
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil {
		return 0, false
	}
	var kind string
	var count *int32
	if json.Unmarshal(event["type"], &kind) != nil || kind != contracts.DeepseekHarnessEventTurnEnd ||
		json.Unmarshal(event[contracts.MessageMetadataFieldToolUses], &count) != nil || count == nil || *count < 0 {
		return 0, false
	}
	return *count, true
}

func (deepseekHarnessProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return storedSessions(ctx, q)
}

func (deepseekHarnessProvider) ExtractTodoEvent(_ string, raw []byte, _ func() []byte) (todoevents.Event, bool) {
	if !bytes.Contains(raw, []byte(contracts.DeepseekHarnessEventTodoWrite)) {
		return todoevents.Event{}, false
	}
	var event struct {
		Type string `json:"type"`
		Data struct {
			Todos *[]struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != contracts.DeepseekHarnessEventTodoWrite || event.Data.Todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*event.Data.Todos))
	for index, row := range *event.Data.Todos {
		status := todoevents.StatusUnspecified
		switch row.Status {
		case "pending":
			status = todoevents.StatusPending
		case "in_progress":
			status = todoevents.StatusInProgress
		case "completed":
			status = todoevents.StatusCompleted
		default:
			return todoevents.Event{}, false
		}
		items = append(items, todoevents.Item{ID: strconv.Itoa(index + 1), Content: row.Content, Status: status})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

// ChildCapabilities permits operations only for a continuable native child.
func (deepseekHarnessProvider) ChildCapabilities(options optionmap.Map) agent.ChildCapabilities {
	accepts := options[contracts.DeepseekHarnessOptionChildMode] == "continuable"
	return agent.ChildCapabilities{AcceptsMessages: accepts, AcceptsInterrupt: accepts}
}
