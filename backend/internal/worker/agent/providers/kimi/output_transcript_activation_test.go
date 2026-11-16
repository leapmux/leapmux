package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeActivationMarkersRetainFactsWithoutStartingATurn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ marker, payload string }{
		{"skill", `{"agentId":"main","activationId":"activation-1","skillName":"review","trigger":"user-slash","skillArgs":"staged changes","skillPath":"/project/.agents/skills/review/SKILL.md","skillSource":"project"}`},
		{"skill", `{"agentId":"main","activationId":"activation-2","pluginId":"checks","commandName":"review","commandArgs":"staged changes","trigger":"user-slash","variant":"plugin_command"}`},
		{"cron.fired", `{"origin":{"kind":"cron_job","jobId":"job-1","cron":"*/5 * * * *","recurring":true,"coalescedCount":0,"stale":false},"prompt":"Check the build."}`},
	} {
		t.Run(test.payload, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			item := fmt.Sprintf(`{"kind":"marker","markerId":"live-m1","marker":%q,"at":"2026-11-16T00:00:00Z","payload":%s}`, test.marker, test.payload)
			op := `{"op":"marker.upsert","item":` + item + `}`
			rig.transcriptOps(t, 1, op)
			rig.transcriptOps(t, 2, op)
			rig.transcriptReset(t, 3, item)
			rows := rig.sink.PersistedNotifications()
			require.Len(t, rows, 1, "live delivery and reset replay retain one marker")
			var stored struct {
				Payload struct {
					Ops []struct {
						Item json.RawMessage `json:"item"`
					} `json:"ops"`
				} `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(rows[0].Content, &stored))
			require.Len(t, stored.Payload.Ops, 1)
			assert.JSONEq(t, item, string(stored.Payload.Ops[0].Item))
			assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
			assert.Empty(t, rig.sink.Messages())
		})
	}
}

func TestKimiNativeActivationMarkerRetriesARefusedWrite(t *testing.T) {
	t.Parallel()
	base := &agenttest.Sink{}
	base.UpdateSessionID("session_1")
	sink := &failedConfigWarningSink{Sink: base, failure: errors.New("notification write refused")}
	a := newOfflineKimiAgent(t, base)
	a.sink = agent.NewProviderServices(sink)
	op := `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"skill","payload":{"agentId":"main","activationId":"a1","skillName":"review","trigger":"model-tool"}}}`
	rig := &kimiOutputRig{agent: a}
	rig.transcriptOps(t, 1, op)
	assert.Empty(t, base.PersistedNotifications())
	assert.Empty(t, a.run("main").transcript.storedMarkers)
	sink.failure = nil
	rig.transcriptOps(t, 2, op)
	rig.transcriptOps(t, 3, op)
	assert.Len(t, base.PersistedNotifications(), 1)
}

func TestKimiNativeActivationMarkersKeepChildOwnership(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	op := `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"skill","payload":{"agentId":"agent-0","activationId":"a1","skillName":"review","trigger":"nested-skill"}}}`
	rig.transcriptAgentOps(t, "agent-0", 1, op)
	assert.Empty(t, rig.sink.PersistedNotifications())
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Len(t, child.PersistedNotifications(), 1)
	assert.Empty(t, rig.sink.PersistedNotifications())
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.transcriptAgentOps(t, "agent-0", 2, op)
	assert.Len(t, child.PersistedNotifications(), 1)
}

func TestKimiNativeActivationMarkersRejectForeignSessionsAndDiscardedOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	op := json.RawMessage(`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"cron.fired","payload":{"origin":{"kind":"cron_job","jobId":"job-1"},"prompt":"Check."}}}`)
	for _, sessionID := range []string{"", "foreign"} {
		frame, err := json.Marshal(map[string]any{
			"type": "transcript.ops", "session_id": sessionID,
			"payload": map[string]any{"type": "transcript.ops", "agent_id": "main", "seq": 1, "ops": []json.RawMessage{op}},
		})
		require.NoError(t, err)
		rig.agent.HandleOutput(frame)
	}
	assert.Empty(t, rig.sink.PersistedNotifications())
	rig.agent.DiscardOutput()
	rig.transcriptOps(t, 1, string(op))
	assert.Empty(t, rig.sink.PersistedNotifications())
}

func TestKimiNativeCronMarkersKeepRepeatedFirings(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for seq := int64(1); seq <= 3; seq++ {
		rig.transcriptOps(t, seq, fmt.Sprintf(`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m%d","marker":"cron.fired","payload":{"origin":{"kind":"cron_job","jobId":"job-1","cron":"*/5 * * * *","recurring":true,"coalescedCount":2,"stale":true},"prompt":"Check the build."}}}`, seq))
	}
	assert.Len(t, rig.sink.PersistedNotifications(), 3, "a job ID does not identify one firing")
}

func TestKimiNativeActivationResetRespectsTheAttachmentTime(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.attachedAt = time.Date(2026, time.November, 16, 0, 0, 0, 0, time.UTC)
	for seq, at := range []string{"2026-11-15T23:59:59Z", "invalid", "2026-11-16T00:00:00Z"} {
		rig.transcriptReset(t, int64(seq+1), fmt.Sprintf(`{"kind":"marker","markerId":"live-m%d","marker":"skill","at":%q,"payload":{"agentId":"main","activationId":"activation-1","skillName":"review","trigger":"model-tool"}}`, seq, at))
	}
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
}
