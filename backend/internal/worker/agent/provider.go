package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/leapmux/leapmux/util/validate"
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
	// IsInterrupt reports whether raw input contains a provider interrupt
	// frame. The normal frontend path uses the InterruptAgent RPC instead.
	IsInterrupt(content string) bool
	// ResolveOptionConflicts merges requested values over current values and
	// settles any pair of this provider's own option values that cannot hold at
	// once. The service calls it before it writes the optimistic option map, on
	// both the launch path and the settings-edit path.
	ResolveOptionConflicts(current, requested optionmap.Map) optionmap.Map
	// PlanModePermissionMode returns the permission mode an APPROVED plan-mode
	// transition of the given kind switches the agent to, when the user selected
	// none in the approval banner.
	//
	// This is a provider decision because the vocabulary is the provider's own:
	// Claude Code says `plan`/`acceptEdits`, ZCode says `plan`/`build`, and Codex
	// says `on-request`. Shared code that stamped one provider's spelling would
	// persist a mode the others reject, and the agent would then report a mode its
	// session never entered.
	//
	// Returns "" for a provider whose PlanModeControl is always None; such a
	// provider never reaches a plan-mode transition, and the caller leaves the
	// mode alone.
	PlanModePermissionMode(kind PlanModeControlKind) string
	// IsSelfDisplayingControlTool reports whether a control response for the
	// named control request (`toolName` is a Claude tool name; other providers
	// ignore it) is ALREADY displayed by the provider's own transcript -- e.g.
	// Claude re-emits AskUserQuestion / ExitPlanMode answers as a user-envelope
	// tool_result. When true, the scroll rail marks that ingested row directly
	// and the service layer persists NO separate structured control-response row
	// (which would double the dot). Every provider except Claude has the service
	// synthesize the structured row and so returns false -- confirmed against the
	// Codex, OpenCode/ACP, and Pi wire protocols, none of which echo a control
	// answer back into their output stream.
	IsSelfDisplayingControlTool(toolName string) bool
	// PlanModeControl classifies a provider-native control request name into
	// the provider-neutral plan-mode operation the service layer should run.
	// Unknown or non-plan controls return PlanModeControlNone.
	PlanModeControl(toolName string) PlanModeControlKind
	// ResolveControlResponse interprets a frontend control response against the
	// stored provider-native control request. It is pure: providers may normalize
	// the response bytes and prune the request into the minimal render context
	// persisted alongside it (plus plan-mode metadata), but the service owns
	// persistence, control-request deletion, option changes, and process I/O.
	ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution
	// ControlResponseRequestID extracts the stored-control-request lookup id from a raw
	// frontend control response, so the service can find the pending control_request row to
	// answer. Both wire shapes it reads -- the neutral approve/reject envelope
	// ({response:{request_id, ...}}, emitted by buildAllowResponse/buildDenyResponse for EVERY
	// provider) and a top-level JSON-RPC id (used by the ACP family and Codex) -- are
	// cross-provider, so every provider delegates to defaultControlResponseRequestID. The method
	// exists so the lookup is provider-owned dispatch rather than wire parsing in shared service
	// code; no provider narrows it, because narrowing to one shape would break the other's flows.
	ControlResponseRequestID(content []byte) string
	// PlanApprovalOptions declares the option changes to settle when a plan-mode-prompt
	// control request is approved (see PlanApprovalOptions). The service applies them; the
	// provider owns the ids/values. Empty for providers with no plan-approval options.
	PlanApprovalOptions() PlanApprovalOptions
	// SyntheticInterruptNotice returns the display text of the synthetic user row the service
	// persists when the frontend forwards this provider's interrupt frame as a raw message
	// (SendAgentRawMessage). Non-empty only for providers that consume the interrupt SILENTLY:
	// Codex resolves turn/interrupt internally and emits no transcript row for it, so without the
	// synthetic row the interrupt would leave no trace. A provider whose interrupt already
	// surfaces in its own transcript returns "" (no synthetic row).
	SyntheticInterruptNotice() string
	// PermissionModeFromRawInput extracts an eager permission-mode update from a raw control
	// frame in the provider's wire format (Claude's set_permission_mode control_request). The
	// service owns the DB write and the raw forward to the subprocess; the provider owns only the
	// parse. Returns ("", false) for providers whose mode changes never ride a raw control frame.
	PermissionModeFromRawInput(content string) (string, bool)
	// ValidateAttachment enforces the provider's attachment policy against a classified
	// attachment. A nil return accepts it; a non-nil error rejects the whole send. Providers with
	// no restrictions accept everything.
	ValidateAttachment(attachment classifiedAttachment) error
	// TurnEndToolUses reports how many tool calls the finished turn made, when
	// the provider's turn-end envelope carries the count. Clients suppress the
	// turn-end sound for a zero-tool turn, so a provider that cannot say must
	// return ok=false rather than 0.
	TurnEndToolUses(content []byte) (count int32, ok bool)
	// EndsSubagentTranscript reports whether content, as the LAST message of a
	// SUBAGENT transcript, already announces that the SUBAGENT itself is over.
	//
	// Used to decide whether that transcript already ends in a divider. Claude
	// forwards a subagent's own `result`, so its child transcript closes
	// itself; writing the worker's neutral subagent-end divider on top would
	// stack two rules saying the same thing. Providers whose child transcript
	// simply stops return false and get the neutral divider.
	//
	// The question is "does this close the SUBAGENT", NOT "is this a turn-end
	// envelope". The two differ for a steerable child: a Codex collab thread
	// draws a turn-end divider at the end of EVERY turn and then accepts
	// another, so answering the turn-end question would suppress the divider
	// for exactly the stopped-mid-life child that needs it. Codex therefore
	// keeps the false default although it does forward a turn end.
	//
	// Content-based, not a static capability: the SAME Claude subagent ends
	// with a forwarded result when it completes and with nothing at all when it
	// is stopped mid-flight, and only the stopped one needs the neutral divider.
	EndsSubagentTranscript(content []byte) bool
	// SupportsChildSteering reports whether a running agent of this provider
	// can address a subagent conversation inside the same process (Codex's
	// collab child threads). Drives AgentInfo.accepts_messages for child tabs:
	// a child of a steering provider keeps an enabled composer; every other
	// child tab is read-only. Defaults to false (noopProvider); only Codex
	// overrides it to true.
	SupportsChildSteering() bool
	// ReportsDefaultModelSentinel reports whether this provider's own model
	// catalog lists DefaultModelSentinel as a selectable entry meaning "the
	// account default". Only such a provider gives that entry the default badge
	// (see defaultModelIDForList). Another provider may report an ordinary model
	// literally id'd "default", and its badge must stay where the catalog put it.
	// Defaults to false (noopProvider); only Claude Code overrides it to true.
	ReportsDefaultModelSentinel() bool
	// ResolveResumeHandle checks the client-supplied handle and returns the
	// value that must reach argv, or reports why this provider cannot resume
	// from it.
	//
	// It RETURNS the handle rather than only judging it, and the caller must
	// pass on what it returns. A rule that NORMALIZES before it judges --
	// `validate.SanitizePath`, which the path shape uses, strips control
	// characters and trims whitespace -- otherwise approves one string while
	// the caller hands the process a different one. Pi opens a session file
	// without requiring that it exists, so that split silently started an empty
	// session at a filename nobody typed. One return value makes the checked
	// string and the sent string the same string.
	//
	// A resume handle is NOT one shape across providers, which is why this is a
	// provider decision rather than one rule in shared code. Claude, Codex and
	// the ACP providers issue an opaque TOKEN -- a UUID, a ULID, a thread id --
	// and Claude's reaches `claude --resume <id>` as its own argv element. Pi
	// accepts a session FILE PATH as well as a token, and the token rule
	// refuses a path by design: a Windows path holds `\\`, and any deep path
	// runs past the token byte cap. Applying the token rule to every provider
	// therefore refused a legitimate Pi resume with "session ID contains
	// invalid characters".
	//
	// The default is the token rule (noopProvider), so a provider that issues a
	// token is covered by saying nothing, and only a provider whose handle is
	// something else has to say so.
	//
	// `homeDir` is for a provider whose handle is a PATH and therefore may open
	// with `~`. A token provider ignores it. The caller has it either way, and
	// OpenAgent already hands the same value to `normalizeWorkingDir`.
	ResolveResumeHandle(handle, homeDir string) (string, error)
	// ListStoredSessions enumerates the resumable sessions this provider's OWN
	// storage holds for the query's working directory, newest first.
	//
	// A provider decision because every CLI keeps its history in a different
	// place and a different shape: a SQLite index (Codex, OpenCode, Kilo,
	// Goose, ZCode), one SQLite file per session (Cursor), a directory named
	// after a mangled copy of the working directory (Claude, Pi), or a sidecar
	// beside each transcript (Reasonix, Copilot). Which of those to read, and
	// where the title and the last-activity time sit inside it, is exactly the
	// knowledge that must not leak into shared code.
	//
	// It reads files another program owns, so an implementation must never
	// write to one, and must report the empty result for a store that is
	// absent, unreadable, or shaped differently than the version it was written
	// against. An error is for a fault the caller could act on; the caller
	// still degrades to what it knows without this provider's answer.
	//
	// The default (noopProvider) lists nothing, which is right for a provider
	// whose sessions this worker cannot enumerate.
	ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error)
	// ExtractTodoEvent derives a to-do list mutation from one persisted message,
	// or reports that the message changes nothing.
	//
	// This is a provider decision because the to-do list rides each provider's own
	// message shape and nothing else: Claude's `TodoWrite` tool_use envelope and its
	// incremental `Task*` family, Codex's `turn/plan/updated` notification, an ACP
	// `sessionUpdate=plan`, and ZCode's `tool.updated` event. The default
	// (noopProvider) reports nothing, which is right for a provider whose CLI states
	// no to-do list at all.
	//
	// It runs on EVERY persisted message, so each implementation states its own
	// cheap discriminator first -- a span-type switch, or a byte search for the
	// method name. There is no shared pre-filter: one would have to know every
	// provider's markers, which is the coupling this method removes.
	//
	// `spanType` is the message's span type (a tool name where the provider sets
	// one, empty for a top-level notification) and `content` is its decompressed
	// body. `pairedToolUse` resolves the body of the tool_use message that opened
	// this row's span, for a provider whose RESULT half does not repeat the input it
	// needs; it costs a database read, so it is a function that only the parsers
	// which need it call, and it returns nil when there is no such message.
	ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool)
}

type noopProvider struct{}

func (noopProvider) Classify(json.RawMessage) NotificationClassification {
	return NotificationClassification{}
}

func (noopProvider) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (noopProvider) IsInterrupt(string) bool { return false }

// ExtractTodoEvent defaults to NO to-do list. A provider whose CLI states one
// overrides this with the shape that carries it.
func (noopProvider) ExtractTodoEvent(string, []byte, func() []byte) (todoevents.Event, bool) {
	return todoevents.Event{}, false
}

// ResolveResumeHandle defaults to the TOKEN rule, which is what every provider
// but Pi issues. `validate.ValidateSessionID` states that rule and says why each
// half of it exists -- above all the leading hyphen, which one argv element is
// enough to turn into a flag. The token rule refuses rather than normalizes, so
// an accepted handle comes back exactly as it arrived.
func (noopProvider) ResolveResumeHandle(handle, _ string) (string, error) {
	if err := validate.ValidateSessionID(handle); err != nil {
		return "", err
	}
	return handle, nil
}

// ListStoredSessions defaults to NO sessions: a provider whose store this
// worker cannot read is covered by saying nothing, and the caller still offers
// whatever the worker's own database recorded.
func (noopProvider) ListStoredSessions(context.Context, StoredSessionQuery) ([]StoredSession, error) {
	return nil, nil
}

// ResolveOptionConflicts defaults to a plain merge: a provider whose option axes are
// independent has no conflict to settle.
func (noopProvider) ResolveOptionConflicts(current, requested optionmap.Map) optionmap.Map {
	return current.Merge(requested)
}

// IsSelfDisplayingControlTool defaults to false: a provider that doesn't echo control
// answers into its own transcript relies on the service layer's synthetic display row.
// The ACP-based providers inherit this via their noopProvider embedding.
func (noopProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (noopProvider) PlanModeControl(string) PlanModeControlKind { return PlanModeControlNone }

// PlanModePermissionMode defaults to "", which pairs with the PlanModeControlNone above:
// a provider that recognizes no plan-mode tool never reaches a transition, so it has no
// target mode to state. A provider that overrides PlanModeControl must override this too.
func (noopProvider) PlanModePermissionMode(PlanModeControlKind) string { return "" }

// PlanApprovalOptions defaults to none: a provider with no plan-mode-prompt flow settles no
// options on approval. The ACP-based providers inherit this via their noopProvider embedding.
func (noopProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// SyntheticInterruptNotice defaults to "": a provider whose interrupt surfaces in its own
// transcript (or that is interrupted via the InterruptAgent RPC rather than a raw frame) needs no
// synthetic notice. The ACP-based providers inherit this via their noopProvider embedding.
func (noopProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput defaults to ("", false): a provider whose permission-mode changes
// don't ride raw control frames carries no eager-parse path. The ACP-based providers inherit this
// via their noopProvider embedding.
func (noopProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

func (noopProvider) TurnEndToolUses(content []byte) (int32, bool) {
	return defaultTurnEndToolUses(content)
}

// EndsSubagentTranscript defaults to false: a provider that forwards no
// subagent-final envelope into its subagent transcripts leaves them to be
// closed by the worker's neutral subagent-end divider. Only Claude overrides
// it.
//
// Codex keeps this default deliberately although it DOES forward a divider:
// its per-turn `turn/completed` ends a turn, not the subagent, and a collab
// child accepts another turn afterwards. Answering true there would suppress
// the closing divider for every stopped child.
func (noopProvider) EndsSubagentTranscript([]byte) bool { return false }

// SupportsChildSteering defaults to false: a provider whose running agents
// cannot steer a subagent conversation inside their own process. Only Codex
// overrides it to true (its collab child threads accept host-initiated turns).
func (noopProvider) SupportsChildSteering() bool { return false }

// ReportsDefaultModelSentinel defaults to false: a provider whose CLI reports
// concrete model ids only must keep the default badge on the entry its own
// catalog designates, even when one of those ids happens to be "default".
func (noopProvider) ReportsDefaultModelSentinel() bool { return false }

// defaultTurnEndToolUses reads a top-level "num_tool_uses" number. Every
// provider shipped today puts it there, but the decision stays behind the
// interface: the moment one does not, its plugin overrides instead of a
// package-level helper growing a switch (see CLAUDE.md).
func defaultTurnEndToolUses(content []byte) (int32, bool) {
	var env struct {
		NumToolUses *int32 `json:"num_tool_uses"`
	}
	if err := json.Unmarshal(content, &env); err != nil || env.NumToolUses == nil {
		return 0, false
	}
	return *env.NumToolUses, true
}

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

// ProviderOrDefault resolves the provider a request asked for to the provider
// the worker will actually run.
//
// One site, because two handlers that answer for the same tab must agree about
// which CLI a request means. OpenAgent spawns Claude Code for a request that
// omits the field, so a listing handler that took the field literally would
// report no resumable sessions and then let OpenAgent resume one of them.
func ProviderOrDefault(provider leapmuxv1.AgentProvider) leapmuxv1.AgentProvider {
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	}
	return provider
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
// PermissionModeStoredSentinel is the literal an OLDER agents.options row can carry for a
// provider that never had a mode named "default". It is a cross-provider DB value, so it
// is spelled here and NOT as contracts.ClaudeModeDefault: that constant means Claude
// Code's own Default mode, and reading it while deciding about a Codex or ZCode row would
// claim Claude's vocabulary governs a provider that never used it. The two strings
// coincide, and that coincidence is not a shared meaning.
const PermissionModeStoredSentinel = "default"

func PermissionModeOrDefault(provider leapmuxv1.AgentProvider, mode string) string {
	defaultMode := FallbackPermissionMode(provider)
	if mode == "" {
		return defaultMode
	}
	if mode == PermissionModeStoredSentinel && defaultMode != "" && defaultMode != PermissionModeStoredSentinel {
		return defaultMode
	}
	return mode
}

// codexProvider embeds noopProvider so it inherits the TurnEndToolUses default
// (Codex puts num_tool_uses at the envelope top level, like every shipped
// provider). Override the method here only if Codex's shape diverges.
type codexProvider struct {
	noopProvider
}

// ListStoredSessions reads Codex's own rollout index; see codex_sessions.go.
func (codexProvider) ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return codexStoredSessions(ctx, q)
}

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

// Codex consumes control responses internally (only a serverRequest/resolved
// metadata notification returns), so it never self-displays the answer.
func (codexProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (codexProvider) PlanModeControl(toolName string) PlanModeControlKind {
	if toolName == ToolNameCodexPlanModePrompt {
		return PlanModeControlPrompt
	}
	return PlanModeControlNone
}

// PlanModePermissionMode answers for Codex's one plan-mode kind, the prompt. An approval
// that selects no mode returns to Codex's own default approval policy; `acceptEdits` and
// `plan` are Claude words that Codex's `--ask-for-approval` rejects.
func (codexProvider) PlanModePermissionMode(kind PlanModeControlKind) string {
	if kind == PlanModeControlPrompt {
		return CodexDefaultApprovalPolicy
	}
	return ""
}

// PlanApprovalOptions settles Codex on plan approval: Base resets the collaboration axis to its
// default mode; Bypass (applied only on a permission-mode switch) grants full network access and
// removes the sandbox for the approved mode.
func (codexProvider) PlanApprovalOptions() PlanApprovalOptions {
	return PlanApprovalOptions{
		Base:   map[string]string{CodexOptionCollaborationMode: CodexCollaborationDefault},
		Bypass: contracts.CodexPlanBypassOptions(),
	}
}

// SyntheticInterruptNotice: Codex resolves turn/interrupt internally and emits only a
// serverRequest/resolved metadata notification -- never a transcript row -- so the service
// persists this synthetic row to record the interrupt. The literal's single home lives here.
func (codexProvider) SyntheticInterruptNotice() string { return "[Request interrupted by user]" }

// PermissionModeFromRawInput: Codex has no set_permission_mode raw control frame.
func (codexProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

// SupportsChildSteering: Codex collab child threads accept host-initiated turns
// inside the same process (turn/steer / turn/start / turn/interrupt on a child
// threadId), so a child tab keeps an enabled composer and SendAgentMessage to a
// child routes through the owner process's ChildSteerer.
func (codexProvider) SupportsChildSteering() bool { return true }

// ReportsDefaultModelSentinel is false: Codex stores the sentinel until the
// thread/start lifecycle response reports a concrete model, and model/list never
// returns it, so Codex badges the model the CLI itself marks.
func (codexProvider) ReportsDefaultModelSentinel() bool { return false }

type claudeProvider struct {
	noopProvider
}

// ListStoredSessions reads Claude Code's own transcripts; see
// claude_sessions.go.
// ReportsDefaultModelSentinel is true: the Claude CLI lists a "default" entry in
// its own initialize response, and convertClaudeModels owns that reserved id, so
// the sentinel is a real selectable option that tracks the account's default
// across plan tiers.
func (claudeProvider) ReportsDefaultModelSentinel() bool { return true }

func (claudeProvider) ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return claudeStoredSessions(ctx, q)
}

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

// PlanModePermissionMode gives Claude Code's own two modes. An approved exit lands on
// `acceptEdits`, which is what the plan banner's unchecked state means for Claude: run
// the plan, and do not ask again for each edit.
func (claudeProvider) PlanModePermissionMode(kind PlanModeControlKind) string {
	switch kind {
	case PlanModeControlEnter:
		return contracts.ClaudeModePlan
	case PlanModeControlExit:
		return contracts.ClaudeModeAcceptEdits
	case PlanModeControlNone, PlanModeControlPrompt:
		return ""
	}
	return ""
}

// Claude's plan flow is EnterPlanMode/ExitPlanMode (never PlanModeControlPrompt), so no
// plan-approval option settlement runs for it.
func (claudeProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// EndsSubagentTranscript recognizes Claude's final `{"type":"result",...}`.
// With --forward-subagent-text a subagent's own result is forwarded into the
// child transcript, and a Claude subagent gets exactly one, so a subagent that
// runs to completion already closes itself and needs no neutral divider
// stacked on top. A subagent stopped mid-flight forwards no result, so its
// transcript does not end here and still gets the neutral divider.
func (claudeProvider) EndsSubagentTranscript(content []byte) bool {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &env); err != nil {
		return false
	}
	return env.Type == claudeMsgTypeResult
}

// SyntheticInterruptNotice: Claude's interrupt surfaces in its own transcript, so no synthetic
// notice is persisted for a forwarded interrupt frame.
func (claudeProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput parses Claude's set_permission_mode control_request
// ({"request":{"subtype":"set_permission_mode","mode":"..."}}) and returns the requested mode.
// Returns ("", false) when the frame isn't a set_permission_mode request. The service eagerly
// writes the returned mode to the DB (so /clear, which reads the DB, sees the latest mode -- Claude
// doesn't echo the mode back in its control_response) and still forwards the raw frame to the
// subprocess.
func (claudeProvider) PermissionModeFromRawInput(content string) (string, bool) {
	if !strings.Contains(content, "set_permission_mode") {
		return "", false
	}
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return "", false
	}
	if msg.Request.Subtype != "set_permission_mode" || msg.Request.Mode == "" {
		return "", false
	}
	return msg.Request.Mode, true
}

// piProvider collapses Pi's lifecycle notifications and recognizes
// Pi's interrupt frame. Pi emits compaction_start/end whenever a turn
// crosses the compaction threshold; without consolidation, long sessions
// accumulate one notification per cycle. auto_retry_start/end follow the
// same pattern as Claude's api_retry. extension_error stays
// unconsolidated: each error message is meaningful and merging would hide
// partial failures.
type piProvider struct {
	noopProvider
}

// ListStoredSessions reads Pi's own transcripts; see pi_sessions.go. It returns
// each session's ID rather than its file path, which is the form Pi reports at
// runtime and therefore the form that dedupes against the worker's own record.
func (piProvider) ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return piStoredSessions(ctx, q)
}

// piResumeHandleIsFilePath reports whether a Pi resume handle identifies a session
// FILE rather than a session ID.
//
// The test copies Pi's own resolver (`resolveSessionPath` in pi's main.ts): a
// separator anywhere, or the `.jsonl` suffix. The two answers must stay
// identical, because this decides which rule validates a handle and Pi decides
// which lookup consumes it. A value that one reads as a path and the other as
// an ID is validated against a rule that does not describe what happens to it.
func piResumeHandleIsFilePath(handle string) bool {
	return strings.ContainsAny(handle, `/\`) || strings.HasSuffix(handle, ".jsonl")
}

// ResolveResumeHandle takes EITHER a session file PATH or a session ID.
//
// Pi identifies one session two ways, and `pi --session <path|id>` resolves
// both: a value that holds a separator or ends in `.jsonl` is a path, and
// anything else is matched against the session IDs of this working directory.
// The worker hands the handle to that flag (see `piResumeArgs`), so both shapes
// are legitimate input here.
//
// Two shapes need two rules, and each rule refuses the other shape. A path is
// not a token: a Windows path holds `\`, which the token class bans, and a real
// Pi session path -- an escaped copy of the working directory plus a
// timestamped file name -- runs past the 128-byte token cap, so the token rule
// refused every legitimate session file with "session ID contains invalid
// characters". An ID is not a path: it is relative by construction, so the path
// rule refused the identifier Pi itself reports with "path must be absolute".
//
// A path is still a value a user pastes into a field, so it is not unchecked:
// `SanitizePath` answers the traversal, the reserved device name and the
// absolute-path questions that a path raises, and the byte cap is the token
// cap's counterpart for the longer shape. The empty handle means "no resume"
// and is accepted, exactly as the token rule accepts it.
//
// The PATH shape returns SanitizePath's result, not the handle. SanitizePath
// normalizes before it judges -- it drops control characters, trims edge
// whitespace, expands `~` and cleans the path -- so the string it approved and
// the string the user typed differ whenever any of those applied. Pi's
// SessionManager.open does not require the file to exist, so sending the typed
// string started an EMPTY session at a filename that had a stray control
// character in it, and the user's conversation was simply gone. Returning the
// approved string removes the gap rather than restating the rule at the sink.
func (piProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	if handle == "" {
		return "", nil
	}
	if !piResumeHandleIsFilePath(handle) {
		if err := validate.ValidateSessionID(handle); err != nil {
			return "", err
		}
		return handle, nil
	}
	// Measured before SanitizePath, which expands `~` and can therefore only
	// make the value longer than what the user typed.
	if len(handle) > contracts.SessionFilePathByteLimit {
		return "", fmt.Errorf("session file path: must be at most %d bytes", contracts.SessionFilePathByteLimit)
	}
	// An invisible-format character survives SanitizePath -- U+200B is Cf, not
	// a control character -- so a path that carries one would reach Pi and open
	// a different file. The token rule refuses the same class, and refusing it
	// here keeps one answer for both shapes of one field.
	if err := validate.RefuseInvisibleSessionChars(handle); err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	sanitized, err := validate.SanitizePath(handle, homeDir)
	if err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	return sanitized, nil
}

func (piProvider) Classify(raw json.RawMessage) NotificationClassification {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return NotificationClassification{}
	}
	switch env.Type {
	case contracts.PiEventCompactionEnd:
		// The boundary signal — repeated boundaries collapse so the chat
		// shows one marker for "the conversation was compacted at this
		// point", not a sequence.
		return NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "pi:" + contracts.PiEventCompactionEnd}
	case contracts.PiEventCompactionStart:
		// In-progress indicator. Latest wins so the UI shows "compacting…"
		// once, not once per attempt.
		return NotificationClassification{Kind: NotificationKindStatus, Key: "pi:" + contracts.PiEventCompactionStart}
	case contracts.PiEventAutoRetryStart, contracts.PiEventAutoRetryEnd:
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

// Pi consumes extension_ui_response on stdin without echoing the answer to stdout,
// so it never self-displays a control answer.
func (piProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (piProvider) PlanModeControl(string) PlanModeControlKind { return PlanModeControlNone }

// Pi has no plan-mode-prompt flow, so it settles no options on approval.
func (piProvider) PlanApprovalOptions() PlanApprovalOptions { return PlanApprovalOptions{} }

// SyntheticInterruptNotice: Pi's abort surfaces in its own transcript, so no synthetic notice is
// persisted for a forwarded interrupt frame.
func (piProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput: Pi has no set_permission_mode raw control frame.
func (piProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

// acpProvider recognizes ACP's `session/cancel` notification (and
// the bare `cancel` form retained for legacy producers). Shared across all
// ACP-based providers (Cursor, Copilot, Kilo, OpenCode, Goose).
// ACP doesn't consolidate notifications today, so Classify/Merge inherit
// the no-op embedding.
type acpProvider struct {
	noopProvider
	provider leapmuxv1.AgentProvider
	// questionRequestContext prunes an OpenCode-protocol `question.asked` request to the minimal
	// context persisted alongside the native answer (the question headers the frontend labels its
	// values with). Non-nil ONLY for the ACP providers that speak that question protocol (OpenCode,
	// Kilo); nil for the rest, whose control answers fall through to the ACP permission context.
	// Set at registration (init) so the "who uses the OpenCode question shape" membership lives at
	// one site (mirroring the frontend's registerOpenCodeProtocolProvider) rather than a
	// provider-enum switch in ResolveControlResponse that would drift.
	questionRequestContext func(requestPayload []byte) json.RawMessage
	// validateAttachment enforces a restrictive attachment policy for the ACP providers that need
	// one (Reasonix is text-only). Non-nil ONLY for those providers; nil accepts everything (the
	// default for Cursor, Copilot, Kilo, OpenCode, Goose). Set at registration (init) so the
	// per-provider policy lives at one site rather than a provider-enum switch.
	validateAttachment func(classifiedAttachment) error
	// listStoredSessions reads this provider's own session store. Non-nil for
	// every ACP provider, because each of the six keeps a store this worker can
	// read -- but each keeps it in a different place and shape, so the function
	// lives in that provider's own file and is wired here at registration, the
	// way validateAttachment already is. Nil lists nothing.
	listStoredSessions func(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error)
	// resolveOptionConflicts settles this provider's own mutually exclusive option
	// values. Non-nil ONLY for Copilot, whose Assisted Approval and Allow All axes
	// exclude each other; nil merges with no conflict rule. The rule reads that
	// provider's own vocabulary, so the function lives in that provider's file and is
	// wired here at registration, the way listStoredSessions above already is.
	resolveOptionConflicts func(current, requested optionmap.Map) optionmap.Map
}

// ListStoredSessions dispatches to the reader the registration supplied. The
// nil check is what keeps `acpProvider` provider-neutral: this method knows
// that ACP providers have stores, and nothing about where any of them is.
func (p acpProvider) ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	if p.listStoredSessions == nil {
		return nil, nil
	}
	return p.listStoredSessions(ctx, q)
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

func (p acpProvider) ResolveOptionConflicts(current, requested optionmap.Map) optionmap.Map {
	if p.resolveOptionConflicts != nil {
		return p.resolveOptionConflicts(current, requested)
	}
	return p.noopProvider.ResolveOptionConflicts(current, requested)
}

func init() {
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, codexProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, piProvider{})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, listStoredSessions: cursorStoredSessions})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, listStoredSessions: copilotStoredSessions, resolveOptionConflicts: resolveCopilotOptionConflicts})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, questionRequestContext: opencodeQuestionRequestContext, listStoredSessions: kiloStoredSessions})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, questionRequestContext: opencodeQuestionRequestContext, listStoredSessions: opencodeStoredSessions})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, listStoredSessions: gooseStoredSessions})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, validateAttachment: reasonixValidateAttachment, listStoredSessions: reasonixStoredSessions})
	RegisterProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, zcodeProvider{})
}
