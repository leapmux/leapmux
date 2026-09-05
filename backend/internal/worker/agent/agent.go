package agent

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// stdoutScannerStartBuf is the scanner's initial buffer. It grows on
// demand up to the ceiling below, so this only decides how many
// reallocations a long line costs.
const stdoutScannerStartBuf = 1024 * 1024

// stdoutConfiguredMax is the worker's configured application payload budget
// (the raise-floor for the live scanner ceiling when no channel is open).
//
// A Hub↔Worker pair always negotiates one effective budget
// (min(hub, worker)) for every channel open, so we keep a refcount of open
// channel ids plus a single negotiated value — not a per-channel budget map.
// While any channel is open the live ceiling is min(configured, negotiated);
// when the last channel closes it rises back to configured.
// stdoutMaxTokenSize is the live scanner token ceiling read by the
// ScanLines wrapper without holding stdoutMu.
var (
	stdoutMu            sync.Mutex
	stdoutConfiguredMax = contracts.MaxMessageSize
	stdoutOpenChannels  = map[string]struct{}{}
	stdoutNegotiatedMax int // meaningful only while len(stdoutOpenChannels) > 0
	stdoutMaxTokenSize  atomic.Int64
)

func init() {
	stdoutMaxTokenSize.Store(int64(contracts.MaxMessageSize))
}

func recomputeStdoutMaxLocked() {
	min := stdoutConfiguredMax
	if len(stdoutOpenChannels) > 0 && stdoutNegotiatedMax < min {
		min = stdoutNegotiatedMax
	}
	stdoutMaxTokenSize.Store(int64(min))
}

// ConfigureMaxMessageSize sets the worker's configured application payload
// budget (0 resolves to the protocol default) and recomputes the live
// scanner ceiling against any open negotiated budget. Bootstrap calls
// this once before agents start.
func ConfigureMaxMessageSize(n int) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutConfiguredMax = channelwire.ResolveMaxMessageSize(n)
	recomputeStdoutMaxLocked()
}

// ObserveNegotiatedMaxMessageSize records that channelID is open at the
// Hub↔Worker negotiated payload budget and sets the live scanner ceiling
// to min(configured, negotiated). Call on successful HandleOpen. Every
// open on this worker pair carries the same effective budget; the
// channel id is tracked only so Release can drop the refcount.
func ObserveNegotiatedMaxMessageSize(channelID string, n int) {
	n = channelwire.ResolveMaxMessageSize(n)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutOpenChannels[channelID] = struct{}{}
	stdoutNegotiatedMax = n
	recomputeStdoutMaxLocked()
}

// ReleaseNegotiatedMaxMessageSize drops channelID from the open set (on
// HandleClose). When the last channel closes, the live ceiling rises back
// to the worker configured budget — including Hub reject-after-accept
// paths that tear the worker session down.
func ReleaseNegotiatedMaxMessageSize(channelID string) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	delete(stdoutOpenChannels, channelID)
	if len(stdoutOpenChannels) == 0 {
		stdoutNegotiatedMax = 0
	}
	recomputeStdoutMaxLocked()
}

// ReleaseAllNegotiatedMaxMessageSizes clears every open-channel ref (CloseAll)
// and restores the live ceiling to the configured budget.
func ReleaseAllNegotiatedMaxMessageSizes() {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	stdoutOpenChannels = map[string]struct{}{}
	stdoutNegotiatedMax = 0
	recomputeStdoutMaxLocked()
}

// liveStdoutMaxTokenSize returns the current scanner token ceiling.
func liveStdoutMaxTokenSize() int {
	return int(stdoutMaxTokenSize.Load())
}

// newStdoutScanner returns the line scanner every agent provider reads
// its subprocess stdout with.
//
// The Buffer capacity ceiling is the worker's configured payload budget
// (an upper bound that never shrinks below what operators allow). The
// live negotiated min is enforced by the SplitFunc on every Scan so a
// later Observe can shrink (or a Release can raise) without recreating
// already-running scanners — bufio.Scanner freezes Buffer's maxTokenSize
// at creation time.
//
// Shared rather than repeated per provider because it is scanner
// plumbing bound to a shared limit, with nothing provider-specific in
// it. What IS provider-specific -- how each one interprets the lines --
// stays in that provider, which is where the read loop the caller hands
// this to lives.
func newStdoutScanner(stdout io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(stdout)
	stdoutMu.Lock()
	configured := stdoutConfiguredMax
	stdoutMu.Unlock()
	start := stdoutScannerStartBuf
	if start > configured {
		start = configured
	}
	scanner.Buffer(make([]byte, 0, start), configured)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		max := liveStdoutMaxTokenSize()
		if max < 1 {
			max = 1
		}
		// ScanLines first so a buffer of many short lines totaling > max
		// still yields the first short token. ErrTooLong only when a single
		// line (complete token, or incomplete line with no newline yet)
		// exceeds the live ceiling — otherwise a later Observe shrink would
		// still admit an oversized in-progress line that Buffer's configured
		// max would allow.
		advance, token, err = bufio.ScanLines(data, atEOF)
		if err != nil {
			return 0, nil, err
		}
		if token != nil && len(token) > max {
			return 0, nil, bufio.ErrTooLong
		}
		if advance == 0 && !atEOF && len(data) > max {
			return 0, nil, bufio.ErrTooLong
		}
		return advance, token, nil
	})
	return scanner
}

// encodeDataURI builds a data URI from a MIME type and raw bytes.
func encodeDataURI(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// SpanInfo groups span-related metadata for a persisted message.
type SpanInfo struct {
	ParentSpanID    string
	ConnectorSpanID string
	SpanID          string
	SpanType        string
	SpanColor       int32
	Closing         bool
	// MarkType rides the persist path into messages.mark_type, tagging the row
	// as a scroll-rail jump target. Zero value (MARK_TYPE_UNSPECIFIED) leaves the
	// row unmarked, so existing SpanInfo{...} literals need no change.
	MarkType leapmuxv1.MarkType
	// NoSpan marks a row that carries a SpanID but deliberately owns no span --
	// a subagent spawn. Its SpanColor of 0 is the ANSWER, not a gap to fill, so
	// the persist path must not substitute the connector's color for it. Without
	// this the spawn card takes a rail color that matches no rail anywhere,
	// whenever its ParentSpanID happens to name a span that is still open.
	NoSpan bool
}

// openToolSpan persists a tool call's opening row and opens its span.
//
// A call that spawns a subagent owns no span: it reserves no color and opens
// nothing, so it draws no rail and its card takes the neutral border. It still
// records its span type, which a provider's closing message reads back.
//
// Each provider decides `spawns` itself, from its own wire shape -- that
// decision must not move into shared code. What lives here is the ORDER the
// decision drives, which every provider needs identically: reserve before the
// persist so the row carries the color its rail will use, persist before the
// open so the row sits at the parent's depth, record the type either way.
//
// The parent span id is empty at every call site: a provider's tool calls are
// flat in the transcript that holds them.
//
// A failed persist is logged by the caller and does NOT stop the span from
// opening, which is what each of these call sites did before.
func openToolSpan(sink OutputSink, content []byte, spanID, spanType string, spawns bool) error {
	var spanColor int32
	if !spawns {
		spanColor = sink.ReserveSpanColor(spanID, "")
	}
	err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, SpanInfo{
		SpanID: spanID, SpanType: spanType, SpanColor: spanColor, NoSpan: spawns,
	})
	sink.SetSpanType(spanID, spanType)
	if !spawns {
		sink.OpenSpan(spanID, "")
	}
	return err
}

type AutoContinueReason string

const (
	AutoContinueReasonAPIError  AutoContinueReason = "api_error"
	AutoContinueReasonRateLimit AutoContinueReason = "rate_limit"
)

type AutoContinueSchedule struct {
	Reason        AutoContinueReason
	DueAt         time.Time
	SourcePayload []byte
}

// scheduleOrCancelAPIErrorAutoContinue schedules an immediate API-error
// auto-continue when retry is true, or cancels any pending API-error
// schedule otherwise. The payload is defensively copied because the
// caller's buffer may be reused by the stdout reader before the schedule
// is consumed.
func scheduleOrCancelAPIErrorAutoContinue(sink OutputSink, retry bool, payload []byte) {
	if !retry {
		sink.CancelAutoContinue(AutoContinueReasonAPIError)
		return
	}
	sink.ScheduleAutoContinue(AutoContinueSchedule{
		Reason:        AutoContinueReasonAPIError,
		DueAt:         time.Now().UTC(),
		SourcePayload: append([]byte(nil), payload...),
	})
}

// OutputSink provides generic primitives for persisting and broadcasting
// agent output. Implemented by the service layer and injected into providers.
type OutputSink interface {
	PersistMessage(source leapmuxv1.MessageSource, content []byte, span SpanInfo) error
	// PersistNotification persists an agent notification (appending it to the
	// active notification thread when one is open). It returns whether the
	// notification produced a frontend-visible broadcast: a flapping notification
	// that collapses byte-identically into the existing thread tail is persisted
	// without a broadcast, and callers (the thinking-token reset decorator) use
	// this to stay in lockstep with the frontend, which only clears on a broadcast.
	PersistNotification(source leapmuxv1.MessageSource, content []byte) (broadcast bool, err error)
	// PersistTurnEnd persists the agent's turn-end divider envelope and
	// fires the sink-level git-status auto-broadcast. Each provider's
	// closing envelope (Claude type:"result", Codex turn/completed,
	// ACP prompt response, Pi agent_end) routes here so that turn-end-
	// specific side effects are explicit at the call site.
	PersistTurnEnd(content []byte, span SpanInfo) error
	OpenSpan(spanID string, parentSpanID string)
	// CloseSpan frees a span's column. The recorded span type SURVIVES it (only
	// ResetSpans clears types), because a provider's closing message reads that
	// type back through GetSpanType. A provider that states a spawn only AFTER
	// its tool call started (Kilo's first in-progress tool_call_update, Claude's
	// task_started for a Workflow run) calls this to give back the span it
	// already opened, while the call itself keeps running.
	CloseSpan(spanID string)
	ResetSpans()
	SetSpanType(spanID, spanType string)
	GetSpanType(spanID string) string
	ReserveSpanColor(spanID, parentSpanID string) int32
	BroadcastStreamChunk(content []byte, spanID string, method string)
	BroadcastStreamEnd(spanID string)
	// PersistControlRequest stores the pending control request and returns the fresh per-instance
	// claim_token it minted for it. The caller threads that token straight into the paired
	// BroadcastControlRequest so the live broadcast carries the SAME token that was persisted --
	// without a second DB round-trip to read it back (and without the readback-failure window that
	// would broadcast an empty token). Empty only when the sink mints none (test fakes).
	PersistControlRequest(requestID string, payload []byte) (claimToken string)
	DeleteControlRequest(requestID string)
	// BroadcastControlRequest fans the control request out to live windows, carrying the claim_token
	// PersistControlRequest returned so the frontend can echo it in its answer (AgentControlRequest.claim_token).
	BroadcastControlRequest(requestID string, payload []byte, claimToken string)
	BroadcastControlCancel(requestID string)
	UpdateSessionID(sessionID string)
	UpdatePermissionMode(mode string)
	// NotifyPermissionModeChanged emits the chat-view settings_changed notification
	// for a permission-mode transition WITHOUT persisting the mode or broadcasting a
	// StatusChange. Used when a combined config_option_update already persisted and
	// broadcast the new settings (model/primary-agent together with the new mode) and
	// only the chat notification remains. A no-op when oldMode is empty or unchanged.
	NotifyPermissionModeChanged(oldMode, newMode string)
	// PersistSettingsRefresh folds the option values an agent reported back into the
	// persisted options row as an optionmap.Map DELTA -- the shared merge contract (a
	// non-empty value is set, an empty value DELETES the key, an ABSENT key is preserved;
	// see the optionmap package doc). A provider therefore omits an axis it cannot report
	// (Claude omits permissionMode to keep the stored value, including a startup-time
	// set_permission_mode; ACP omits effort) and sends concrete values for everything it manages.
	PersistSettingsRefresh(refresh optionmap.Map)
	BroadcastStatusActive(sessionID string)
	BroadcastSessionInfo(info map[string]interface{})
	PersistLeapMuxNotification(content map[string]interface{})
	StorePlanModeToolUse(toolUseID, targetMode string)
	LoadAndDeletePlanModeToolUse(toolUseID string) (targetMode string, ok bool)
	UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string)
	ScheduleAutoContinue(schedule AutoContinueSchedule)
	CancelAutoContinue(reason AutoContinueReason)

	// --- Subagent transcripts and the background-task registry ---

	// EnsureChildAgent resolves (and creates on first sight) the virtual child
	// agent spawned by the tool_use span spawnSpanID in THIS sink's transcript,
	// linked to the provider's child key. Idempotent across replays and worker
	// restarts. providerChildKey doubles as the registry row_key when non-empty.
	//
	// title is the MODEL's, so the sink cleans it with validate.CleanName --
	// the same rule every other writer of a title column applies, and the one
	// place that documents its steps. A provider passes what it has, and does
	// not cap or strip it first. Pass "" when the provider has no title: the
	// sink keeps the registry row's current title and gives the agent row a
	// pooled fallback name.
	EnsureChildAgent(spawnSpanID, providerChildKey, title string) (childAgentID string, err error)

	// ChildSink returns an OutputSink bound to the child agent's transcript.
	// The child sink has its OWN span tracker; transcript primitives act on the
	// child. Registry primitives on a child sink write under the same ROOT owner.
	ChildSink(childAgentID string) OutputSink

	// PersistChildMessage / PersistChildTurnEnd are shorthands for
	// ChildSink(id).PersistMessage / PersistTurnEnd.
	PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error
	PersistChildTurnEnd(childAgentID string, content []byte, span SpanInfo) error

	// PersistChildPrompt writes the spawn prompt as the FIRST message of the
	// child transcript, so the subagent tab opens on the instruction the
	// subagent was given instead of on its first reply. Persisted as a USER
	// message carrying {"content": prompt}, which is the same envelope a typed
	// message uses -- so it renders as markdown, in full, with no per-provider
	// shape for the frontend to learn.
	//
	// Idempotent by emptiness: a no-op when the child transcript already holds
	// a message, and when prompt is blank. A re-attach after a worker restart
	// therefore cannot duplicate the prompt, and a provider that learns the
	// prompt only AFTER the subagent has started talking does not wedge it
	// below the transcript it is supposed to introduce.
	PersistChildPrompt(childAgentID, prompt string) error

	// PersistChildUserMessage APPENDS text to a child transcript as a USER
	// message, in the same {"content": text} envelope PersistChildPrompt uses.
	// For a message the parent delivered to a subagent mid-transcript, where
	// PersistChildPrompt cannot help: that one opens the transcript and is a
	// no-op once the subagent spoke.
	//
	// Carries MARK_TYPE_USER_MESSAGE, which the opening prompt does not: this
	// message lands in the MIDDLE of a transcript, and a mid-transcript user
	// message is exactly what the scroll rail exists to find.
	//
	// A no-op for a blank text or an empty child id. The envelope shape stays
	// here, with the other transcript primitives, so no provider encodes it.
	PersistChildUserMessage(childAgentID, text string) error

	// CleanupChildAgent releases the per-child service state (span tracker,
	// todos cache, cached child sink) for a child that has reached a final
	// state. A provider calls this once a subagent closes for good so a
	// long-running root that cycles many children does not accumulate a stale
	// SpanTracker + sink ref per closed child until the root itself closes.
	// Idempotent. The child AGENT row and its transcript survive (they live
	// under the root and re-hydrate on reopen); only the in-memory caches are
	// reclaimed. A no-op for an unknown child id.
	CleanupChildAgent(childAgentID string)

	// Registry writes. All are keyed under the ROOT owner of this sink.

	// UpsertBackgroundTask cleans Title with validate.CleanName, the rule that
	// every title column in the worker shares. A command handed over as the
	// title keeps every character of itself, quoting included, so the row
	// labels the command that ran. A Title that the rule empties says what a
	// blank Title says: the row keeps the title it already holds. A provider
	// that has a better fallback for that case applies it BEFORE it calls
	// (acpBridge's terminal/create falls back to "shell").
	UpsertBackgroundTask(task bgtask.Upsert) error
	UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error
	CloseBackgroundTask(rowKey string, status bgtask.Status) error
	// RenameBackgroundTask atomically re-keys a row from oldKey to newKey,
	// preserving its status, child linkage, and final state. Used when a
	// provider learns the stable child id only on the final update (OpenCode:
	// the row opens under the toolCallId and the session id surfaces late). A
	// no-op when oldKey is absent or newKey is empty.
	RenameBackgroundTask(oldKey, newKey string) error

	// LookupBackgroundTask resolves a provider row key to the child transcript it
	// owns and the row's current status. ok is false when the root's registry
	// holds no such row, and the returned status is meaningless then -- the zero
	// Status is StatusPending, a real value, not an "unknown" sentinel.
	//
	// err is non-nil when the registry could not be READ at all, which is a third
	// answer and not a miss. A caller must not treat it as "no such row": the two
	// are indistinguishable in the return values, and the decisions that turn on
	// a miss (this is a first start; there is nothing to revive) are all wrong
	// when the truth is "unknown".
	//
	// This is how a provider turns an id the MODEL supplied into one of its own
	// subagents. The registry is the right index for it: it is keyed by row key,
	// it carries the child linkage, and it survives a provider's own bookkeeping
	// being dropped at task end, a worker restart, AND the display cap -- a row
	// that carries a child transcript outlives the capped list the sidebar
	// shows, so the thousandth subagent of a session resolves exactly like the
	// first. A key that identifies nothing (a display name, another session, a
	// foreign address) simply misses.
	LookupBackgroundTask(rowKey string) (childAgentID string, status bgtask.Status, ok bool, err error)

	// ReviveBackgroundTask returns a FINISHED row to Running, clears its
	// ended_at, and lets the reopened transcript be closed again by the next
	// completion. A no-op for an absent row and for one that is already active,
	// so a duplicate revive is harmless.
	//
	// Call this ONLY with positive evidence that the provider restarted the task.
	// Every other registry write treats a final status as absorbing, because a
	// replayed running update cannot prove a restart and honoring it would leave
	// a row Running that nothing closes -- which pins the parent's thinking
	// indicator for good. This method is the one deliberate exception, and the
	// burden of proof sits with its caller.
	ReviveBackgroundTask(rowKey string) error
}

// logRegistryRefusal records a background-task write the registry REFUSED.
//
// `bgtask.ValidateRowKey` turned an unusable provider key from a silent rewrite
// into an error, so every one of these writes gained a failure mode it did not
// have before -- and every provider takes its key straight from the agent's own
// JSON with no length bound of its own. A bare `_ =` therefore meant a refused
// row simply never appeared in the sidebar, or a finished subagent never left
// the Running state, with nothing anywhere to say why: the failure mode the
// refusal was chosen to AVOID, moved from the data to the diagnosis.
//
// It lives here, beside the sink interface, rather than once per provider. The
// error belongs to the SINK's rule and not to any provider's wire format, and a
// helper each provider writes for itself is one the next provider forgets --
// which is what happened: Pi grew one and the other three did not.
//
// The write stays best-effort. A refused row must not fail the event around it.
func logRegistryRefusal(provider, op string, err error) {
	if err != nil {
		slog.Warn("background task write refused", "provider", provider, "op", op, "error", err)
	}
}

// OptionSettlementState identifies a confirmed or unresolved option.
type OptionSettlementState uint8

const (
	OptionSettlementConfirmed OptionSettlementState = iota + 1
	OptionSettlementUnresolved
)

// OptionSettlement reports whether the provider confirmed one option. A
// confirmed settlement with a nil value removes the option.
type OptionSettlement struct {
	State OptionSettlementState
	Value *string
}

type OptionSettlements map[string]OptionSettlement

// SettingsApplyResult reports whether the provider applied a change without a
// restart. SurfacedOptions is the provider's complete live snapshot.
type SettingsApplyResult struct {
	AppliedLive     bool
	Settlements     OptionSettlements
	SurfacedOptions optionmap.Map
}

func (r SettingsApplyResult) ConfirmedOptions() optionmap.Map {
	confirmed := make(optionmap.Map)
	for id, settlement := range r.Settlements {
		if settlement.State == OptionSettlementConfirmed && settlement.Value != nil && *settlement.Value != "" {
			confirmed[id] = *settlement.Value
		}
	}
	return confirmed
}

func confirmedSettings(options map[string]string) SettingsApplyResult {
	settlements := make(OptionSettlements, len(options))
	surfaced := make(optionmap.Map, len(options))
	for id, value := range options {
		surfaced[id] = value
		value := value
		settlements[id] = OptionSettlement{State: OptionSettlementConfirmed, Value: &value}
	}
	return SettingsApplyResult{AppliedLive: true, Settlements: settlements, SurfacedOptions: surfaced}
}

func restartRequiredSettings(options optionmap.Map) SettingsApplyResult {
	settlements := make(OptionSettlements, len(options))
	for id := range options {
		settlements[id] = OptionSettlement{State: OptionSettlementUnresolved}
	}
	return SettingsApplyResult{Settlements: settlements}
}

// Agent is the interface that all coding agent providers must implement.
type Agent interface {
	AgentID() string
	// SendInput delivers a user message and returns once it is DELIVERED --
	// never when the resulting turn ends. Every provider states delivery the
	// same way, whatever its wire protocol:
	//
	//   - A protocol with no acknowledgement (Claude, over stdin): the write to
	//     the process IS the delivery. Return once it lands.
	//   - A protocol that acknowledges (Codex's `turn/started`): return on the
	//     ack, or report the delivery unconfirmed after a bounded wait. Send the
	//     request itself without waiting on its response, because a protocol
	//     whose response arrives at TURN END answers a different question.
	//
	// Nothing here may block for the length of a turn. The worker acks the
	// client's RPC and broadcasts the user's message only after this returns, so
	// a provider that waits for the turn makes the browser -- whose deadline is
	// far shorter -- label a delivered message "Failed to deliver", and shows
	// every watcher the assistant's reply before the message that caused it.
	// It also parks the caller's goroutine, and any lifecycle work behind it,
	// for the same span.
	//
	// A turn-level failure is reported afterwards, out of band, through the
	// provider's own error notification -- not by holding this call open.
	SendInput(content string, attachments []*leapmuxv1.Attachment) error
	SendRawInput(data []byte) error
	Stop()
	IsStopped() bool
	DiscardOutput()
	Wait() error
	Stderr() string
	HandleOutput(content []byte)
	// OptionGroups returns every agent configuration axis (model, effort,
	// permission mode, and provider-specific options) as option groups,
	// each carrying its current value, default, mutability, and display order.
	// It is the single source for both the read model (catalog + current value)
	// and the persisted settings (via CurrentOptions).
	OptionGroups() []*leapmuxv1.AvailableOptionGroup
	SettingsSnapshot() SettingsApplyResult
	// UpdateSettings applies each included non-empty option to the running agent.
	// Omitted and empty options do not change the live state. The result distinguishes
	// confirmed values from values that need a restart or a later acknowledgement.
	UpdateSettings(options optionmap.Map) SettingsApplyResult
	// ClearContext starts a new thread/session on the running process,
	// effectively clearing conversation context without a full restart.
	// Returns the new session ID, or ("", false) if the provider does not
	// support in-place context clearing (caller should restart instead).
	ClearContext() (sessionID string, ok bool)
	// Interrupt aborts the agent's current turn using the provider-
	// specific signal (SIGINT, JSON-RPC stop, control payload, etc.).
	// Returns nil on success or if the agent is not currently in a turn;
	// returns a non-nil error only when the interrupt mechanism itself
	// failed (e.g. stdin write error).
	Interrupt() error
}

// ContextCompactor runs a provider-native context compaction. Providers that
// do not expose compaction do not implement this interface.
type ContextCompactor interface {
	CompactContext() error
}

// InputSteerer adds input to an active turn. The queue calls it only for an
// explicit Steer operation. Normal dispatch always starts a later turn.
type InputSteerer interface {
	SteerInput(content string, attachments []*leapmuxv1.Attachment) error
}

type SteeringCapability interface {
	SupportsSteering() bool
}

// InputReadiness reports whether normal queue dispatch can start a turn.
type InputReadiness interface {
	InputReady() bool
}

type inputReadySink interface {
	InputReady()
}

type inputStartedSink interface {
	InputStarted()
}

func notifyInputStarted(sink OutputSink) {
	if started, ok := sink.(inputStartedSink); ok {
		started.InputStarted()
	}
}

func notifyInputReady(sink OutputSink) {
	if ready, ok := sink.(inputReadySink); ok {
		ready.InputReady()
	}
}

var (
	ErrCompactionUnsupported = errors.New("agent provider does not support native context compaction")
	ErrSteeringUnsupported   = errors.New("agent provider does not support steering")
	ErrNoActiveTurn          = errors.New("agent has no active turn to steer")
	ErrDeliveryUncertain     = errors.New("agent input delivery is uncertain")
)

// ChildSteerer lets a provider address a child conversation in its process.
// Queue dispatch resolves the child registry row and drives the owner process.
// Providers that cannot steer a child do not implement this interface.
type ChildSteerer interface {
	// SendChildInput sends a user message to a child conversation identified by
	// childKey (the provider linkage key stored in the registry row_key). Returns
	// ErrChildSteeringUnsupported only via the Manager's type-assertion path.
	SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error
	// SteerChildInput adds input to the child's active turn.
	SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error
	// InterruptChild stops the child's current turn inside the owner process.
	InterruptChild(childKey string) error
}

// ErrChildSteeringUnsupported is returned by the Manager when a running Agent
// does not implement ChildSteerer (the provider cannot steer a subagent). The
// service maps it to FailedPrecondition so the frontend shows a clear queue failure.
var ErrChildSteeringUnsupported = errors.New("agent provider does not support steering a subagent")

// ErrChildNotSteerableYet is returned by a ChildSteerer when the owner process
// is running but does not yet know the child thread (a worker restart emptied
// the in-memory spawn index; the registry row resolves, but the live stream has
// not re-fired the spawn). The service maps it to UNAVAILABLE so the frontend
// re-queues the send/interrupt instead of treating it as a permanent delivery
// failure (SendChildInput) or a missing child (InterruptChild). Distinguished
// from ErrChildSteeringUnsupported: this provider DOES steer, just not yet.
var ErrChildNotSteerableYet = errors.New("subagent not yet steerable in the running owner process; retry")
