package agent

import (
	"encoding/base64"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

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
	PersistControlRequest(requestID string, payload []byte)
	DeleteControlRequest(requestID string)
	BroadcastControlRequest(requestID string, payload []byte)
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
