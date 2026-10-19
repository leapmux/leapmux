package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestPublishCompactionStatesTheTrigger(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	PublishCompaction(sink, "auto")
	PublishCompaction(sink, "")
	assert.Equal(t, []map[string]interface{}{
		{"compaction": map[string]interface{}{"trigger": "auto"}},
		{"compaction": map[string]interface{}{}},
	}, sink.SessionInfos())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestEndCompactionSendsNull(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	EndCompaction(sink)
	assert.Equal(t, []map[string]interface{}{{"compaction": nil}}, sink.SessionInfos())
}

func TestPersistCompactionEndedWritesTheOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome CompactionEndOutcome
		detail  string
		want    map[string]interface{}
	}{
		{CompactionEndFailed, "", map[string]interface{}{"type": "compaction_ended", "outcome": "failed"}},
		{CompactionEndAborted, " \n", map[string]interface{}{"type": "compaction_ended", "outcome": "aborted"}},
		{CompactionEndFailed, "  failed request  ", map[string]interface{}{"type": "compaction_ended", "outcome": "failed", "error": "failed request"}},
		{"unknown", "", map[string]interface{}{"type": "compaction_ended", "outcome": "failed"}},
	} {
		t.Run(string(tc.outcome)+tc.detail, func(t *testing.T) {
			sink := &agenttest.Sink{}
			PersistCompactionEnded(sink, tc.outcome, tc.detail)
			assert.Equal(t, []map[string]interface{}{tc.want}, sink.LeapMuxNotifications())
			assert.Empty(t, sink.SessionInfos())
		})
	}
}
