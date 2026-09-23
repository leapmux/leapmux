package agent

import (
	"context"
	"encoding/json"

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

// Provider bundles the per-provider wire-format hooks the service
// layer invokes without holding a running-agent reference. Plugins are
// stateless and shared across goroutines — a single instance per provider.
//
// This is the backend counterpart to the frontend chat plugin: each agent
// provider has its own JSONL/JSON-RPC frame shape, and the service layer
// dispatches via this interface instead of OR-ing all formats together.
type Provider interface {
	// ResolveProviderData combines native supplemental fields without changing either source.
	ResolveProviderData(MessageContent) []byte
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
	//
	// It must recognize the frame that the provider's own Interrupt writes. Each
	// provider pins that round trip in its
	// TestInterrupt_<Provider>WireFormatMatchesProviderClassifier, so a producer
	// and a detector that diverge fail a test rather than a first incident.
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
	// PlanApprovalOptions resolves the complete settings for an approved plan prompt.
	// An empty permission mode keeps the current mode. Bypass requires the preset's exact mode.
	PlanApprovalOptions(permissionMode string) map[string]string
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
	ValidateAttachment(attachment ClassifiedAttachment) error
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
	// envelope". The two differ for a resumable child: a Codex collab thread
	// draws a turn-end divider after each turn, and its parent can resume it.
	// Answering the turn-end question would suppress the divider
	// for exactly the stopped-mid-life child that needs it. Codex therefore
	// keeps the false default although it does forward a turn end.
	//
	// Content-based, not a static capability: the SAME Claude subagent ends
	// with a forwarded result when it completes and with nothing at all when it
	// is stopped mid-flight, and only the stopped one needs the neutral divider.
	EndsSubagentTranscript(content []byte) bool
	// SupportsChildSteering reports whether a running agent of this provider
	// can address a subagent conversation inside the same process. It drives
	// AgentInfo.accepts_messages for child tabs:
	// a child of a steering provider keeps an enabled composer; every other
	// child tab is read-only. The default is false.
	SupportsChildSteering() bool
	// ReportsDefaultModelSentinel reports whether this provider's own model
	// catalog lists DefaultModelSentinel as a selectable entry meaning "the
	// account default". Only such a provider gives that entry the default badge
	// (see defaultModelIDForList). Another provider may report an ordinary model
	// literally id'd "default", and its badge must stay where the catalog put it.
	// Defaults to false (ProviderDefaults); only Claude Code overrides it to true.
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
	// The default is the token rule (ProviderDefaults), so a provider that issues a
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
	// The default (ProviderDefaults) lists nothing, which is right for a provider
	// whose sessions this worker cannot enumerate.
	ListStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error)
	// ExtractTodoEvent derives a to-do list mutation from one persisted message,
	// or reports that the message changes nothing.
	//
	// This is a provider decision because the to-do list rides each provider's own
	// message shape and nothing else: Claude's `TodoWrite` tool_use envelope and its
	// incremental `Task*` family, Codex's `turn/plan/updated` notification, an ACP
	// `sessionUpdate=plan`, and ZCode's `tool.updated` event. The default
	// (ProviderDefaults) reports nothing, which is right for a provider whose CLI states
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

type ProviderDefaults struct{}

func (ProviderDefaults) ResolveProviderData(content MessageContent) []byte {
	return content.Original
}

func (ProviderDefaults) Classify(json.RawMessage) NotificationClassification {
	return NotificationClassification{}
}

func (ProviderDefaults) Merge(class NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (ProviderDefaults) IsInterrupt(string) bool { return false }

// ExtractTodoEvent defaults to NO to-do list. A provider whose CLI states one
// overrides this with the shape that carries it.
func (ProviderDefaults) ExtractTodoEvent(string, []byte, func() []byte) (todoevents.Event, bool) {
	return todoevents.Event{}, false
}

// ResolveResumeHandle defaults to the TOKEN rule, which is what every provider
// but Pi issues. `validate.ValidateSessionID` states that rule and says why each
// half of it exists -- above all the leading hyphen, which one argv element is
// enough to turn into a flag. The token rule refuses rather than normalizes, so
// an accepted handle comes back exactly as it arrived.
func (ProviderDefaults) ResolveResumeHandle(handle, _ string) (string, error) {
	if err := validate.ValidateSessionID(handle); err != nil {
		return "", err
	}
	return handle, nil
}

// ListStoredSessions defaults to NO sessions: a provider whose store this
// worker cannot read is covered by saying nothing, and the caller still offers
// whatever the worker's own database recorded.
func (ProviderDefaults) ListStoredSessions(context.Context, StoredSessionQuery) ([]StoredSession, error) {
	return nil, nil
}

// ResolveOptionConflicts defaults to a plain merge: a provider whose option axes are
// independent has no conflict to settle.
func (ProviderDefaults) ResolveOptionConflicts(current, requested optionmap.Map) optionmap.Map {
	return current.Merge(requested)
}

// IsSelfDisplayingControlTool defaults to false: a provider that doesn't echo control
// answers into its own transcript relies on the service layer's synthetic display row.
// The ACP-based providers inherit this via their ProviderDefaults embedding.
func (ProviderDefaults) IsSelfDisplayingControlTool(string) bool { return false }

func (ProviderDefaults) PlanModeControl(string) PlanModeControlKind { return PlanModeControlNone }

// PlanModePermissionMode defaults to "", which pairs with the PlanModeControlNone above:
// a provider that recognizes no plan-mode tool never reaches a transition, so it has no
// target mode to state. A provider that overrides PlanModeControl must override this too.
func (ProviderDefaults) PlanModePermissionMode(PlanModeControlKind) string { return "" }

// PlanApprovalOptions defaults to none: a provider with no plan-mode-prompt flow settles no
// options on approval. The ACP-based providers inherit this via their ProviderDefaults embedding.
func (ProviderDefaults) PlanApprovalOptions(string) map[string]string { return nil }

// SyntheticInterruptNotice defaults to "": a provider whose interrupt surfaces in its own
// transcript (or that is interrupted via the InterruptAgent RPC rather than a raw frame) needs no
// synthetic notice. The ACP-based providers inherit this via their ProviderDefaults embedding.
func (ProviderDefaults) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput defaults to ("", false): a provider whose permission-mode changes
// don't ride raw control frames carries no eager-parse path. The ACP-based providers inherit this
// via their ProviderDefaults embedding.
func (ProviderDefaults) PermissionModeFromRawInput(string) (string, bool) { return "", false }

func (ProviderDefaults) TurnEndToolUses(content []byte) (int32, bool) {
	return DefaultTurnEndToolUses(content)
}

// EndsSubagentTranscript defaults to false: a provider that forwards no
// subagent-final envelope into its subagent transcripts leaves them to be
// closed by the worker's neutral subagent-end divider. Only Claude overrides
// it.
//
// Codex keeps this default deliberately although it DOES forward a divider:
// its per-turn `turn/completed` ends a turn, not the subagent, and the parent
// can send the child another turn. Answering true there would suppress
// the closing divider for every stopped child.
func (ProviderDefaults) EndsSubagentTranscript([]byte) bool { return false }

// SupportsChildSteering defaults to false for a provider whose running agents
// cannot send direct input to a child conversation.
func (ProviderDefaults) SupportsChildSteering() bool { return false }

// ReportsDefaultModelSentinel defaults to false: a provider whose CLI reports
// concrete model ids only must keep the default badge on the entry its own
// catalog designates, even when one of those ids happens to be "default".
func (ProviderDefaults) ReportsDefaultModelSentinel() bool { return false }

// DefaultTurnEndToolUses reads a top-level "num_tool_uses" number. Every
// provider shipped today puts it there, but the decision stays behind the
// interface: the moment one does not, its plugin overrides instead of a
// package-level helper growing a switch (see CLAUDE.md).
func DefaultTurnEndToolUses(content []byte) (int32, bool) {
	var env struct {
		NumToolUses *int32 `json:"num_tool_uses"`
	}
	if err := json.Unmarshal(content, &env); err != nil || env.NumToolUses == nil {
		return 0, false
	}
	return *env.NumToolUses, true
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
