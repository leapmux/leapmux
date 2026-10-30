package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiloPromptEndingRefreshesUsageForEveryOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		stopped bool
	}{
		{name: "success"}, {name: "failure", err: assert.AnError}, {name: "stop", stopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newAgent(true)
			a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
			kiloHooks(a).PromptEnded(tc.err, tc.stopped)
			require.Len(t, a.usageRefresh, 1)
			<-a.usageRefresh
			assert.Empty(t, a.usageRefresh)
		})
	}
}
