package kilo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeCompactionRow(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, opencode.EventRoute, opencode.QuestionRoot)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
	a.SetSessionIDForTest("session-1")
	*a.HooksForTest() = opencode.FamilyHooks(&a.FamilyBase)
	read := make(chan struct{})
	a.MemberEvents = func(string, json.RawMessage) { close(read) }
	a.BeginQuestionsForTest(ctx, a.Sink(), "agent-1", daemon.Server.URL)
	daemon.Send("data: {\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"summary\",\"role\":\"assistant\",\"summary\":true}}}\n\n")
	const end = `{"type":"session.compacted","properties":{"sessionID":"session-1"}}`
	daemon.Send("data: " + end + "\n\n")
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"session-1\"}}}\n\n")
	select {
	case <-read:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the daemon bridge did not read its events")
	}
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, end, string(sink.PersistedNotifications()[0].Content))
	assert.Equal(t, agent.CompactionBoundaryNotification(), Registration().Plugin.Classify(sink.PersistedNotifications()[0].Content))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}
