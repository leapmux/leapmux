package agent

import (
	"encoding/json"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type NotificationKind string

const (
	NotificationKindNone               NotificationKind = ""
	NotificationKindStatus             NotificationKind = "status"
	NotificationKindAPIRetry           NotificationKind = "api_retry"
	NotificationKindCompactionBoundary NotificationKind = "compaction_boundary"
	NotificationKindProviderScoped     NotificationKind = "provider_scoped"
)

type NotificationClassification struct {
	Kind NotificationKind
	Key  string
}

func (c NotificationClassification) Consolidatable() bool {
	return c.Kind != NotificationKindNone
}

type NotificationConsolidator interface {
	Classify(raw json.RawMessage) NotificationClassification
	Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error)
}

type noopNotificationConsolidator struct{}

func (noopNotificationConsolidator) Classify(json.RawMessage) NotificationClassification {
	return NotificationClassification{}
}

func (noopNotificationConsolidator) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

var (
	notificationConsolidatorMu       sync.RWMutex
	notificationConsolidatorRegistry = map[leapmuxv1.AgentProvider]NotificationConsolidator{}
)

func RegisterNotificationConsolidator(provider leapmuxv1.AgentProvider, consolidator NotificationConsolidator) {
	notificationConsolidatorMu.Lock()
	defer notificationConsolidatorMu.Unlock()
	notificationConsolidatorRegistry[provider] = consolidator
}

func NotificationConsolidatorForProvider(provider leapmuxv1.AgentProvider) NotificationConsolidator {
	notificationConsolidatorMu.RLock()
	defer notificationConsolidatorMu.RUnlock()
	if consolidator := notificationConsolidatorRegistry[provider]; consolidator != nil {
		return consolidator
	}
	return noopNotificationConsolidator{}
}

type codexNotificationConsolidator struct{}

func (codexNotificationConsolidator) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Method string `json:"method"`
		Params *struct {
			Name string `json:"name,omitempty"`
		} `json:"params,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	switch env.Method {
	case "account/rateLimits/updated":
		return NotificationClassification{
			Kind: NotificationKindProviderScoped,
			Key:  "codex:account/rateLimits/updated",
		}
	case "mcpServer/startupStatus/updated":
		name := "unknown"
		if env.Params != nil && env.Params.Name != "" {
			name = env.Params.Name
		}
		return NotificationClassification{
			Kind: NotificationKindProviderScoped,
			Key:  "codex:mcpServer/startupStatus/updated:" + name,
		}
	default:
		return NotificationClassification{}
	}
}

func (codexNotificationConsolidator) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

type claudeNotificationConsolidator struct{}

func (claudeNotificationConsolidator) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	if env.Type != "system" {
		return NotificationClassification{}
	}
	switch env.Subtype {
	case "status":
		return NotificationClassification{Kind: NotificationKindStatus, Key: "claude:system:status"}
	case "api_retry":
		return NotificationClassification{Kind: NotificationKindAPIRetry, Key: "claude:system:api_retry"}
	case "compact_boundary", "microcompact_boundary":
		return NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "claude:system:" + env.Subtype}
	default:
		return NotificationClassification{}
	}
}

func (claudeNotificationConsolidator) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func init() {
	RegisterNotificationConsolidator(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexNotificationConsolidator{})
	RegisterNotificationConsolidator(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeNotificationConsolidator{})
}
