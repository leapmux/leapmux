package providerkit

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A launch reads the provider's own registered locator, the SAME one the availability scan
// reads, so the two can never disagree about which program runs. The locator's own states
// are pinned in package launch; this pins the provider's half.
func TestResolveProviderLaunch(t *testing.T) {
	// Both non-found states are startup failures: neither can start a process. The error
	// identifies the provider by its display name.
	for _, tc := range []struct {
		name string
		res  launch.Resolution
	}{{"missing", launch.Missing}, {"unknown", launch.Unknown}} {
		t.Run(tc.name+" is an error naming the provider", func(t *testing.T) {
			_, err := ResolveLaunch(context.Background(), agent.Options{Shell: "/bin/sh"},
				leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, agenttest.AnsweringLocator(launch.Spec{}, tc.res))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "ZCode")
		})
	}

}
