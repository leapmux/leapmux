package providerkit

import "github.com/leapmux/leapmux/generated/contracts"

// PermissionCheckServices publishes live state for one transcript's tool calls.
type PermissionCheckServices interface {
	MessageSessionID() string
	BroadcastSessionInfo(map[string]interface{})
}

// PublishPermissionCheck associates the check with the session that owns its tool row.
// An explicit null ends the check. An absent field would preserve the previous state.
// A provider must resolve its native agent identity before it calls this helper.
func PublishPermissionCheck(sink PermissionCheckServices, spanID string, checking bool) {
	if spanID == "" {
		return
	}
	var value interface{}
	if checking {
		value = true
	}
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool: map[string]interface{}{
			contracts.RunningToolFieldSpanId:          spanID,
			contracts.RunningToolFieldAgentSessionId:  sink.MessageSessionID(),
			contracts.RunningToolFieldPermissionCheck: value,
		},
	})
}
