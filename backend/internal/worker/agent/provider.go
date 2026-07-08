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

type PlanModeControlKind int

const (
	PlanModeControlNone PlanModeControlKind = iota
	PlanModeControlEnter
	PlanModeControlExit
	PlanModeControlPrompt
)

// PlanApprovalOptions is the provider-specific option settlement the service applies when a
// plan-mode-prompt control request is APPROVED. Keeping the option ids/values here (rather than
// hardcoded in the shared service layer) means a provider owns its own plan-approval wire values.
//   - Base is applied unconditionally on approval (e.g. Codex settling its collaboration axis).
//   - Bypass is applied only when the approval also switches permission mode (e.g. Codex granting
//     full network access + no sandbox for the approved mode).
//
// Both maps are nil for a provider with no plan-approval options.
type PlanApprovalOptions struct {
	Base   map[string]string
	Bypass map[string]string
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
	// DefaultPermissionMode returns the provider-native default permission
	// mode/approval policy -- the value stamped under the "permissionMode"
	// option id when the agent carries no explicit selection.
	DefaultPermissionMode() string
	// IsSelfDisplayingControlTool reports whether a control response for the
	// named control request (`toolName` is a Claude tool name; other providers
	// ignore it) is ALREADY displayed by the provider's own transcript -- e.g.
	// Claude re-emits AskUserQuestion / ExitPlanMode answers as a user-envelope
	// tool_result. When true, the scroll rail marks that ingested row directly
	// and the service layer persists NO separate synthetic display row (which
	// would double the dot). Every provider except Claude synthesizes the display
	// row itself (persistSyntheticUserMessage) and so returns false -- confirmed
	// against the Codex, OpenCode/ACP, and Pi wire protocols, none of which echo a
	// control answer back into their output stream.
	IsSelfDisplayingControlTool(toolName string) bool
	// PlanModeControl classifies a provider-native control request name into
	// the provider-neutral plan-mode operation the service layer should run.
	// Unknown or non-plan controls return PlanModeControlNone.
	PlanModeControl(toolName string) PlanModeControlKind
	// ResolveControlResponse interprets a frontend control response against the
	// stored provider-native control request. It is pure: providers may normalize
	// the response bytes and compute display/plan metadata, but the service owns
	// persistence, control-request deletion, option changes, and process I/O.
	ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution
	// PlanApprovalOptions declares the option changes to settle when a plan-mode-prompt
	// control request is approved (see PlanApprovalOptions). The service applies them; the
	// provider owns the ids/values. Empty for providers with no plan-approval options.
	PlanApprovalOptions() PlanApprovalOptions
	// NeedsSyntheticInterruptNotice reports whether the service must persist a synthetic
	// "[Request interrupted by user]" user row when the frontend forwards this provider's
	// interrupt frame as a raw message (SendAgentRawMessage). True only for providers that
	// consume the interrupt SILENTLY: Codex resolves turn/interrupt internally and emits no
	// transcript row for it, so without the synthetic row the interrupt would leave no trace.
	// A provider whose interrupt already surfaces in its own transcript returns false.
	NeedsSyntheticInterruptNotice() bool
}

type noopProvider struct{}

func (noopProvider) Classify(json.RawMessage) NotificationClassification {
	return NotificationClassification{}
}

func (noopProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (noopProvider) IsInterrupt(string) bool { return false }

func (noopProvider) DefaultPermissionMode() string { return "" }

// IsSelfDisplayingControlTool defaults to false: a provider that doesn't echo control
// answers into its own transcript relies on the service layer's synthetic display row.
// The ACP-based providers inherit this via their noopProvider embedding.
func (noopProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (noopProvider) PlanModeControl(string) PlanModeControlKind { return PlanModeControlNone }

// PlanApprovalOptions defaults to none: a provider with no plan-mode-prompt flow settles no
// options on approval. The ACP-based providers inherit this via their noopProvider embedding.
func (noopProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// NeedsSyntheticInterruptNotice defaults to false: a provider whose interrupt surfaces in its
// own transcript (or that is interrupted via the InterruptAgent RPC rather than a raw frame)
// needs no synthetic notice. The ACP-based providers inherit this via their noopProvider
// embedding.
func (noopProvider) NeedsSyntheticInterruptNotice() bool { return false }

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

// PermissionModeOrDefault normalizes an empty permission mode to the
// provider-native default. It also treats the historical DB schema default
// "default" as unset for providers whose native default is different.
func PermissionModeOrDefault(provider leapmuxv1.AgentProvider, mode string) string {
	defaultMode := ProviderFor(provider).DefaultPermissionMode()
	if mode == "" {
		return defaultMode
	}
	if mode == PermissionModeDefault && defaultMode != "" && defaultMode != PermissionModeDefault {
		return defaultMode
	}
	return mode
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

func (codexProvider) DefaultPermissionMode() string { return CodexDefaultApprovalPolicy }

// Codex consumes control responses internally (only a serverRequest/resolved
// metadata notification returns), so it never self-displays the answer.
func (codexProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (codexProvider) PlanModeControl(toolName string) PlanModeControlKind {
	if toolName == ToolNameCodexPlanModePrompt {
		return PlanModeControlPrompt
	}
	return PlanModeControlNone
}

// PlanApprovalOptions settles Codex on plan approval: Base resets the collaboration axis to its
// default mode; Bypass (applied only on a permission-mode switch) grants full network access and
// removes the sandbox for the approved mode.
func (codexProvider) PlanApprovalOptions() PlanApprovalOptions {
	return PlanApprovalOptions{
		Base: map[string]string{CodexOptionCollaborationMode: CodexCollaborationDefault},
		Bypass: map[string]string{
			CodexOptionNetworkAccess: CodexNetworkEnabled,
			CodexOptionSandboxPolicy: CodexSandboxDangerFullAccess,
		},
	}
}

// NeedsSyntheticInterruptNotice: Codex resolves turn/interrupt internally and emits only a
// serverRequest/resolved metadata notification -- never a transcript row -- so the service
// persists the synthetic "[Request interrupted by user]" row to record the interrupt.
func (codexProvider) NeedsSyntheticInterruptNotice() bool { return true }

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

func (claudeProvider) DefaultPermissionMode() string { return PermissionModeDefault }

// Claude re-emits AskUserQuestion / ExitPlanMode answers as a user-envelope
// tool_result in its own transcript, so the rail marks that ingested row directly
// (claudeUserEnvelopeMarkType) and no synthetic display row is persisted for them. The single
// home for this set, shared by the mark classifier and the synthetic-row skip.
func (claudeProvider) IsSelfDisplayingControlTool(name string) bool {
	return name == ToolNameAskUserQuestion || name == ToolNameExitPlanMode
}

func (claudeProvider) PlanModeControl(toolName string) PlanModeControlKind {
	switch toolName {
	case ToolNameEnterPlanMode:
		return PlanModeControlEnter
	case ToolNameExitPlanMode:
		return PlanModeControlExit
	default:
		return PlanModeControlNone
	}
}

// Claude's plan flow is EnterPlanMode/ExitPlanMode (never PlanModeControlPrompt), so no
// plan-approval option settlement runs for it.
func (claudeProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// NeedsSyntheticInterruptNotice: Claude's interrupt surfaces in its own transcript, so no
// synthetic notice is persisted for a forwarded interrupt frame.
func (claudeProvider) NeedsSyntheticInterruptNotice() bool { return false }

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

func (piProvider) DefaultPermissionMode() string { return "" }

// Pi consumes extension_ui_response on stdin without echoing the answer to stdout,
// so it never self-displays a control answer.
func (piProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (piProvider) PlanModeControl(string) PlanModeControlKind { return PlanModeControlNone }

// Pi has no plan-mode-prompt flow, so it settles no options on approval.
func (piProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// NeedsSyntheticInterruptNotice: Pi's abort surfaces in its own transcript, so no synthetic
// notice is persisted for a forwarded interrupt frame.
func (piProvider) NeedsSyntheticInterruptNotice() bool { return false }

// acpProvider recognizes ACP's `session/cancel` notification (and
// the bare `cancel` form retained for legacy producers). Shared across all
// ACP-based providers (Cursor, Copilot, Kilo, OpenCode, Goose).
// ACP doesn't consolidate notifications today, so Classify/Merge inherit
// the no-op embedding.
type acpProvider struct {
	noopProvider
	provider              leapmuxv1.AgentProvider
	defaultPermissionMode string
	// questionAnswersText renders the display summary for an OpenCode-protocol `question.asked`
	// answer. Non-nil ONLY for the ACP providers that speak that question protocol (OpenCode,
	// Kilo); nil for the rest, whose control answers fall through to the ACP permission summary.
	// Set at registration (init) so the "who uses the OpenCode question shape" membership lives at
	// one site (mirroring the frontend's registerOpenCodeProtocolProvider) rather than a
	// provider-enum switch in ResolveControlResponse that would drift.
	questionAnswersText func(requestPayload, responseContent []byte) string
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

func (p acpProvider) DefaultPermissionMode() string {
	return p.defaultPermissionMode
}

func init() {
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, piProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, defaultPermissionMode: CursorCLIModeAgent})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, defaultPermissionMode: CopilotCLIModeAgent})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, questionAnswersText: opencodeQuestionAnswersText})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, questionAnswersText: opencodeQuestionAnswersText})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, defaultPermissionMode: GooseCLIModeAuto})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX})
}
