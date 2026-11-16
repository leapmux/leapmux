package kimi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKimiNativeOriginalOriginControlsSteering(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		origin    string
		steerable bool
	}{
		{`{"kind":"user","payload":{"kind":"user"}}`, true},
		{`{"kind":"other","payload":{"kind":"skill_activation","name":"review"}}`, true},
		{`{"kind":"other","payload":{"kind":"plugin_command","name":"review"}}`, true},
		{`{"kind":"cron","payload":{"kind":"cron_job","name":"daily"}}`, false},
		{`{"kind":"other","payload":{"kind":"system_trigger","name":"goal_continuation"}}`, false},
	} {
		t.Run(test.origin, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":%s}}`, test.origin))
			assert.Equal(t, test.steerable, rig.agent.PublishTurnActive().Steerable)
		})
	}
}

func TestKimiNativeAutonomousOriginAndTaskTextAreRetainedOnce(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"cron","payload":{"kind":"cron_job","name":"daily"}}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"task","taskId":"task-1"},"prompt":"Task finished: All tests pass"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","role":"user","frameId":"t0.1.f1","taskId":"task-1","text":"Task finished\nAll tests pass"}}`,
	} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, operation)
			rig.transcriptOps(t, 2, operation)
			assert.Len(t, rig.sink.PersistedNotifications(), 1)
		})
	}
}
