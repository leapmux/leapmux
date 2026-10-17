package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishPermissionCheckUsesTheCurrentSinkSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("first-session")
	PublishPermissionCheck(sink, "call", true)
	first, err := json.Marshal(sink.LastSessionInfo())
	require.NoError(t, err)
	assert.JSONEq(t, `{"running_tool":{"span_id":"call","agent_session_id":"first-session","permission_check":true}}`, string(first))
	sink.UpdateSessionID("second-session")
	PublishPermissionCheck(sink, "call", false)
	second, err := json.Marshal(sink.LastSessionInfo())
	require.NoError(t, err)
	assert.JSONEq(t, `{"running_tool":{"span_id":"call","agent_session_id":"second-session","permission_check":null}}`, string(second))
}

func TestPublishPermissionCheckWithoutASpanDoesNotReadTheSink(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() { PublishPermissionCheck(nil, "", true) })
}
