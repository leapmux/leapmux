package agent

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// SpanInfo groups span-related metadata for a persisted message.
type SpanInfo struct {
	ParentSpanID string
	SpanID       string
	SpanColor    int32
	Closing      bool
}

// OutputSink provides generic primitives for persisting and broadcasting
// agent output. Implemented by the service layer and injected into providers.
type OutputSink interface {
	PersistMessage(role leapmuxv1.MessageRole, content []byte, span SpanInfo) error
	PersistNotification(role leapmuxv1.MessageRole, content []byte) error
	OpenSpan(spanID string, parentSpanID string)
	CloseSpan(spanID string)
	PeekNextSpanColor() int32
	BroadcastStreamChunk(content []byte)
	PersistControlRequest(requestID string, payload []byte)
	DeleteControlRequest(requestID string)
	BroadcastControlRequest(requestID string, payload []byte)
	BroadcastControlCancel(requestID string)
	UpdateSessionID(sessionID string)
	UpdatePermissionMode(mode string)
	BroadcastStatusActive(sessionID string)
	BroadcastSessionInfo(info map[string]interface{})
	BroadcastNotification(content map[string]interface{})
	SoftClearNotifThread()
	StorePlanModeToolUse(toolUseID, targetMode string)
	LoadAndDeletePlanModeToolUse(toolUseID string) (targetMode string, ok bool)
	UpdatePlan(filePath string, content []byte, compression leapmuxv1.ContentCompression, title string)
}

// Provider is the interface that all coding agent providers must implement.
type Provider interface {
	AgentID() string
	SendInput(content string) error
	SendRawInput(data []byte) error
	Stop()
	IsStopped() bool
	Wait() error
	Stderr() string
	CurrentSettings() *leapmuxv1.AgentSettings
	HandleOutput(content []byte)
	AvailableModels() []*leapmuxv1.AvailableModel
	// UpdateSettings applies setting changes to a running agent so that
	// the next turn picks them up without a restart. Providers that do
	// not support live updates (e.g. Claude Code) return false.
	UpdateSettings(s *leapmuxv1.AgentSettings) bool
}
