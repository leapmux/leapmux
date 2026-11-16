package kimi

import (
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiResetCompactionMarkersReleaseManualInputOnce(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"completed", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			start := `{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"},"at":"2026-11-15T01:00:00Z"}`
			end := fmt.Sprintf(`{"kind":"marker","markerId":"live-m2","marker":"compaction","payload":{"phase":%q,"result":{"tokensBefore":9000,"tokensAfter":1000}},"at":"2026-11-15T01:00:01Z"}`, phase)
			rig.transcriptOps(t, 1, `{"op":"marker.upsert","item":`+start+`}`)
			require.True(t, rig.agent.PublishTurnActive().Active)
			rig.transcriptReset(t, 2, start, end)
			assert.False(t, rig.agent.PublishTurnActive().Active)
			assert.False(t, rig.agent.run("main").compactionLive)
			require.Len(t, rig.sink.PersistedNotifications(), 1)
			rig.transcriptReset(t, 2, start, end)
			assert.Len(t, rig.sink.PersistedNotifications(), 1, "a repeated snapshot must not publish another completion")
			assert.False(t, rig.agent.PublishTurnActive().Active)
		})
	}
}

func TestKimiResetCompactionAbsenceClearsThePreviousLiveState(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"}}}`)
	require.True(t, rig.agent.PublishTurnActive().Active)
	rig.transcriptReset(t, 2)
	assert.False(t, rig.agent.PublishTurnActive().Active)
	assert.False(t, rig.agent.run("main").compactionLive)
	assert.Empty(t, rig.sink.PersistedNotifications(), "absence supplies no completed outcome to display")
}

func TestKimiResetHistoricalCompactionDoesNotPublishAnOldNoticeOrSpinner(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.attachedAt = time.Date(2026, time.November, 15, 2, 0, 0, 0, time.UTC)
	rig.transcriptReset(t, 5,
		`{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"},"at":"2026-11-15T01:00:00Z"}`,
		`{"kind":"marker","markerId":"live-m2","marker":"compaction","payload":{"phase":"completed","result":{"tokensBefore":9000,"tokensAfter":1000}},"at":"2026-11-15T01:00:01Z"}`)
	assert.Empty(t, rig.sink.PersistedNotifications())
	for _, info := range rig.sink.SessionInfos() {
		if value, present := info[contracts.SessionInfoKeyCompaction]; present {
			assert.Nil(t, value, "completed history must not reopen the live indicator")
		}
	}
	assert.False(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiResetCompactionKeepsAnEarlierStartedOperationBusy(t *testing.T) {
	t.Parallel()
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.attachedAt = time.Date(2026, time.November, 15, 2, 0, 0, 0, time.UTC)
			items := []string{`{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"},"at":"2026-11-15T01:00:00Z"}`}
			if blocked {
				items = append(items, `{"kind":"marker","markerId":"live-m2","marker":"compaction","payload":{"phase":"blocked"},"at":"2026-11-15T01:00:01Z"}`)
			}
			rig.transcriptReset(t, 5, items...)
			assert.True(t, rig.agent.PublishTurnActive().Active)
			assert.True(t, rig.agent.run("main").compactionLive)
			assert.Empty(t, rig.sink.PersistedNotifications(), "an unfinished operation has no completion notice")
		})
	}
}

func TestKimiResetCompactionRetainsAnOutcomeAtTheAttachmentTime(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.attachedAt = time.Date(2026, time.November, 15, 2, 0, 0, 0, time.UTC)
	end := `{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"completed","result":{"tokensBefore":9000,"tokensAfter":1000}},"at":"2026-11-15T02:00:00Z"}`
	rig.transcriptReset(t, 5, end)
	rig.transcriptReset(t, 5, end)
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}

func TestKimiResetCompactionFoldsAnAlreadyRetainedCompletion(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	start := `{"kind":"marker","markerId":"live-m1","marker":"compaction","payload":{"phase":"started","trigger":"manual"},"at":"2026-11-15T01:00:00Z"}`
	end := `{"kind":"marker","markerId":"live-m2","marker":"compaction","payload":{"phase":"completed","result":{"tokensBefore":9000,"tokensAfter":1000}},"at":"2026-11-15T01:00:01Z"}`
	rig.transcriptOps(t, 2, `{"op":"marker.upsert","item":`+end+`}`)
	require.Len(t, rig.sink.PersistedNotifications(), 1)
	rig.transcriptReset(t, 3, start, end)
	assert.False(t, rig.agent.PublishTurnActive().Active)
	assert.False(t, rig.agent.run("main").compactionLive)
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}
