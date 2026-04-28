package agent

import (
	"encoding/base64"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// OutputSink provides generic primitives for persisting and broadcasting
// agent output. Implemented by the service layer and injected into providers.
type OutputSink interface {
	PersistMessage(role leapmuxv1.MessageRole, content []byte, span SpanInfo) error
	PersistNotification(role leapmuxv1.MessageRole, content []byte) error
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
	BroadcastSettingsRefreshed(model, effort, permissionMode string, extraSettings map[string]string)
	BroadcastStatusActive(sessionID string)
	BroadcastSessionInfo(info map[string]interface{})
	BroadcastNotification(content map[string]interface{})
	StorePlanModeToolUse(toolUseID, targetMode string)
	LoadAndDeletePlanModeToolUse(toolUseID string) (targetMode string, ok bool)
	UpdatePlan(filePath string, content []byte, compression leapmuxv1.ContentCompression, title string)
	ScheduleAutoContinue(schedule AutoContinueSchedule)
	CancelAutoContinue(reason AutoContinueReason)
}

// Provider is the interface that all coding agent providers must implement.
type Provider interface {
	AgentID() string
	SendInput(content string, attachments []*leapmuxv1.Attachment) error
	SendRawInput(data []byte) error
	Stop()
	IsStopped() bool
	DiscardOutput()
	Wait() error
	Stderr() string
	CurrentSettings() *leapmuxv1.AgentSettings
	HandleOutput(content []byte)
	AvailableModels() []*leapmuxv1.AvailableModel
	AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup
	// UpdateSettings applies setting changes to a running agent so that
	// the next turn picks them up without a restart. Providers that do
	// not support live updates (e.g. Claude Code) return false.
	UpdateSettings(s *leapmuxv1.AgentSettings) bool
	// ClearContext starts a new thread/session on the running process,
	// effectively clearing conversation context without a full restart.
	// Returns the new session ID, or ("", false) if the provider does not
	// support in-place context clearing (caller should restart instead).
	ClearContext() (sessionID string, ok bool)
}
