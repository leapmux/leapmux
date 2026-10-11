//go:build unix

package codex

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexChildInterruptKeepsTheStoredNativeIdentity(t *testing.T) {
	t.Parallel()
	longNativeID := strings.Repeat("x", bgtask.RowKeyByteLimit+1)
	for _, test := range []struct {
		name     string
		nativeID string
	}{
		{name: "plain native ID", nativeID: "child-thread"},
		{name: "long native ID", nativeID: longNativeID},
		{name: "derived-prefix native ID", nativeID: fmt.Sprint(bgtask.NormalizeRowKey(longNativeID))},
		{name: "escaped-prefix native ID", nativeID: "leapmux-escaped-native-key:" + strings.Repeat("a", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newCodexInterruptRig(t)
			rig.agent.SetAPITimeoutForTest(30 * time.Second)
			recording := &agenttest.Sink{}
			rig.agent.sink = agent.NewProviderServices(recording)
			childID, err := rig.agent.sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: test.nativeID, Title: "Native child"})
			require.NoError(t, err)
			rig.agent.collabChildren = map[string]*codexChildState{test.nativeID: {spawnCorrelationID: "spawn", childAgentID: childID, turnID: "native-turn"}}
			rows := recording.BackgroundTasks()
			require.Len(t, rows, 1)
			require.NoError(t, rig.agent.InterruptChild(rows[0].RowKey, agent.StopContext{}))
			frames := rig.captured()
			require.Len(t, frames, 1)
			assert.Equal(t, "turn/interrupt", frames[0]["method"])
			params, ok := frames[0]["params"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, test.nativeID, params["threadId"])
			assert.Equal(t, "native-turn", params["turnId"])
		})
	}
}
