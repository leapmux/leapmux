package agent

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/channelwire"
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
	stdoutConfiguredMax = channelwire.MaxMessageSize
	stdoutOpenChannels  = map[string]struct{}{}
	stdoutNegotiatedMax int // meaningful only while len(stdoutOpenChannels) > 0
	stdoutMaxTokenSize  atomic.Int64
)

func init() {
	stdoutMaxTokenSize.Store(int64(channelwire.MaxMessageSize))
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
	// terminal envelope (Claude type:"result", Codex turn/completed,
	// ACP prompt response, Pi agent_end) routes here so that turn-end-
	// specific side effects are explicit at the call site.
	PersistTurnEnd(content []byte, span SpanInfo) error
	OpenSpan(spanID string, parentSpanID string)
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
	EnsureChildAgent(spawnSpanID, providerChildKey, title string) (childAgentID string, err error)

	// ChildSink returns an OutputSink bound to the child agent's transcript.
	// The child sink has its OWN span tracker; transcript primitives act on the
	// child. Registry primitives on a child sink write under the same ROOT owner.
	ChildSink(childAgentID string) OutputSink

	// PersistChildMessage / PersistChildTurnEnd are shorthands for
	// ChildSink(id).PersistMessage / PersistTurnEnd.
	PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error
	PersistChildTurnEnd(childAgentID string, content []byte, span SpanInfo) error

	// CleanupChildAgent releases the per-child service state (span tracker,
	// todos cache, cached child sink) for a child that has reached a terminal
	// state. A provider calls this once a subagent closes for good so a
	// long-running root that cycles many children does not accumulate a stale
	// SpanTracker + sink ref per closed child until the root itself closes.
	// Idempotent. The child AGENT row and its transcript survive (they live
	// under the root and re-hydrate on reopen); only the in-memory caches are
	// reclaimed. A no-op for an unknown child id.
	CleanupChildAgent(childAgentID string)

	// Registry writes. All are keyed under the ROOT owner of this sink.
	UpsertBackgroundTask(task bgtask.Upsert) error
	UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error
	CloseBackgroundTask(rowKey string, status bgtask.Status) error
	// RenameBackgroundTask atomically re-keys a row from oldKey to newKey,
	// preserving its status, child linkage, and terminal state. Used when a
	// provider learns the stable child id only on the terminal update (OpenCode:
	// the row opens under the toolCallId and the session id surfaces late). A
	// no-op when oldKey is absent or newKey is empty.
	RenameBackgroundTask(oldKey, newKey string) error
}

// Agent is the interface that all coding agent providers must implement.
type Agent interface {
	AgentID() string
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
	// UpdateSettings applies the agent's FULL current option map (id->value) to a running
	// agent so the next turn picks the change up without a restart. The service always passes
	// the COMPLETE merged map, NOT a sparse delta; implementers compare each axis against the
	// running value and push only what differs. An EMPTY value means "no value for this axis"
	// and is IGNORED -- it is NOT a delete here. (The empty-value-deletes rule of optionmap.Map
	// applies only to the persistence/merge path -- optionsChangeDelta + casPersistAgentOptions --
	// never to this in-memory apply.) Returns false when the change requires a restart (e.g. a
	// Claude effort->auto transition).
	UpdateSettings(options optionmap.Map) bool
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

// ChildSteerer is optionally implemented by a running Agent whose provider can
// address a child conversation inside the same process (Codex's collab child
// threads). It is the backend hook behind "send a message to a subagent":
// SendAgentMessage resolves the child's registry row, then drives the owner
// process through this interface. Providers that cannot steer a subagent
// (Claude, ACP, Pi) simply do not implement it; the service rejects a send to
// such a child with FailedPrecondition instead of crashing.
type ChildSteerer interface {
	// SendChildInput sends a user message to a child conversation identified by
	// childKey (the provider linkage key stored in the registry row_key). Returns
	// ErrChildSteeringUnsupported only via the Manager's type-assertion path.
	SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error
	// InterruptChild aborts the child's current turn inside the owner process.
	InterruptChild(childKey string) error
}

// ErrChildSteeringUnsupported is returned by the Manager when a running Agent
// does not implement ChildSteerer (the provider cannot steer a subagent). The
// service maps it to FailedPrecondition so the frontend shows the composer
// gate rather than a delivery-error bubble.
var ErrChildSteeringUnsupported = errors.New("agent provider does not support steering a subagent")

// ErrChildNotSteerableYet is returned by a ChildSteerer when the owner process
// is running but does not yet know the child thread (a worker restart emptied
// the in-memory spawn index; the registry row resolves, but the live stream has
// not re-fired the spawn). The service maps it to UNAVAILABLE so the frontend
// re-queues the send/interrupt instead of treating it as a permanent delivery
// failure (SendChildInput) or a missing child (InterruptChild). Distinguished
// from ErrChildSteeringUnsupported: this provider DOES steer, just not yet.
var ErrChildNotSteerableYet = errors.New("subagent not yet steerable in the running owner process; retry")
