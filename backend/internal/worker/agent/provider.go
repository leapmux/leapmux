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

// Provider bundles the per-provider wire-format hooks the service
// layer invokes without holding a running-agent reference. Plugins are
// stateless and shared across goroutines — a single instance per provider.
//
// This is the backend counterpart to the frontend chat plugin: each agent
// provider has its own JSONL/JSON-RPC frame shape, and the service layer
// dispatches via this interface instead of OR-ing all formats together.
type Provider interface {
	// Classify categorizes a persisted notification frame for consolidation
	// in consolidateNotificationThread. Frames the plugin doesn't recognize
	// return NotificationClassification{} (Consolidatable() == false).
	Classify(raw json.RawMessage) NotificationClassification
	// Merge combines two notifications previously classified into the same
	// group. The default keeps the newer entry verbatim; providers override
	// when they want a richer reduction (e.g. accumulating retry counts).
	Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error)
	// IsInterrupt reports whether content is an interrupt-request frame in
	// the provider's wire format. This is the inverse of the frontend
	// plugin's buildInterruptContent — the producer and parser pair live
	// at opposite ends of the same wire.
	IsInterrupt(content string) bool
}

type noopProvider struct{}

func (noopProvider) Classify(json.RawMessage) NotificationClassification {
	return NotificationClassification{}
}

func (noopProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (noopProvider) IsInterrupt(string) bool { return false }

var (
	providerMu       sync.RWMutex
	providerRegistry = map[leapmuxv1.AgentProvider]Provider{}
)

func RegisterProvider(provider leapmuxv1.AgentProvider, plugin Provider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	providerRegistry[provider] = plugin
}

func ProviderFor(provider leapmuxv1.AgentProvider) Provider {
	providerMu.RLock()
	defer providerMu.RUnlock()
	if plugin := providerRegistry[provider]; plugin != nil {
		return plugin
	}
	return noopProvider{}
}

// IsInterruptRequest reports whether content is an interrupt frame in the
// wire format used by provider. Unknown providers and unparseable payloads
// both return false.
func IsInterruptRequest(provider leapmuxv1.AgentProvider, content string) bool {
	return ProviderFor(provider).IsInterrupt(content)
}

type codexProvider struct{}

func (codexProvider) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Method string `json:"method"`
		Params *struct {
			Name string `json:"name,omitempty"`
			Item *struct {
				Type string `json:"type,omitempty"`
			} `json:"item,omitempty"`
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
	case "skills/changed":
		return NotificationClassification{
			Kind: NotificationKindProviderScoped,
			Key:  "codex:skills/changed",
		}
	case "remoteControl/status/changed":
		return NotificationClassification{
			Kind: NotificationKindProviderScoped,
			Key:  "codex:remoteControl/status/changed",
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
	case "thread/compacted":
		return NotificationClassification{
			Kind: NotificationKindCompactionBoundary,
			Key:  "codex:thread/compacted",
		}
	case "item/started":
		// Codex emits item/started for many item kinds; only the
		// contextCompaction subtype is consolidatable as a compacting
		// indicator. All other item types route through the per-item
		// handler and never hit PersistNotification.
		if env.Params != nil && env.Params.Item != nil && env.Params.Item.Type == "contextCompaction" {
			return NotificationClassification{
				Kind: NotificationKindStatus,
				Key:  "codex:item/started:contextCompaction",
			}
		}
		return NotificationClassification{}
	default:
		return NotificationClassification{}
	}
}

func (codexProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (codexProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == "turn/interrupt"
}

type claudeProvider struct{}

func (claudeProvider) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	switch env.Type {
	case NotificationTypeRateLimitEvent:
		// Consolidate by keeping only the latest rate-limit snapshot in
		// the thread; older entries collapse so the UI shows one current
		// status, not a wall of repeated tier updates.
		return NotificationClassification{Kind: NotificationKindProviderScoped, Key: "claude:rate_limit_event"}
	case "system":
		// fall through to the subtype switch below
	default:
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

func (claudeProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (claudeProvider) IsInterrupt(content string) bool {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Request.Subtype == "interrupt"
}

// piProvider collapses Pi's lifecycle notifications and recognizes
// Pi's interrupt frame. Pi emits compaction_start/end whenever a turn
// crosses the compaction threshold; without consolidation, long sessions
// accumulate one notification per cycle. auto_retry_start/end follow the
// same pattern as Claude's api_retry. extension_error stays
// unconsolidated: each error message is meaningful and merging would hide
// partial failures.
type piProvider struct{}

func (piProvider) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	switch env.Type {
	case PiEventCompactionEnd:
		// The boundary signal — repeated boundaries collapse so the chat
		// shows one marker for "the conversation was compacted at this
		// point", not a sequence.
		return NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "pi:" + PiEventCompactionEnd}
	case PiEventCompactionStart:
		// In-progress indicator. Latest wins so the UI shows "compacting…"
		// once, not once per attempt.
		return NotificationClassification{Kind: NotificationKindStatus, Key: "pi:" + PiEventCompactionStart}
	case PiEventAutoRetryStart, PiEventAutoRetryEnd:
		return NotificationClassification{Kind: NotificationKindAPIRetry, Key: "pi:" + env.Type}
	default:
		return NotificationClassification{}
	}
}

func (piProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (piProvider) IsInterrupt(content string) bool {
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Type == "abort"
}

// acpProvider recognizes ACP's `session/cancel` notification (and
// the bare `cancel` form retained for legacy producers). Shared across all
// ACP-based providers (Gemini, Cursor, Copilot, Kilo, OpenCode, Goose).
// ACP doesn't consolidate notifications today, so Classify/Merge inherit
// the no-op embedding.
type acpProvider struct {
	noopProvider
}

func (acpProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == "session/cancel" || msg.Method == "cancel"
}

func init() {
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, piProvider{})
	acp := acpProvider{}
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	} {
		RegisterProvider(p, acp)
	}
}
