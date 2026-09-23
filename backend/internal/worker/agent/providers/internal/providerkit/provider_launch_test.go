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

// A launch reads the provider and locator from the same registration.
func TestResolveProviderLaunch(t *testing.T) {
	registration := agent.Registration{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE}
	resolved := launch.Spec{Program: "zcode", PrefixArgs: []string{"--stdio"}}
	called := false
	registration.Locator = launch.Custom(func(_ context.Context, shell string, loginShell bool) (launch.Spec, launch.Resolution) {
		called = true
		assert.Equal(t, "/bin/sh", shell)
		assert.True(t, loginShell)
		return resolved, launch.Found
	})
	_, err := ResolveLaunch(context.Background(), agent.Options{Shell: "/bin/sh", LoginShell: true}, registration)
	require.NoError(t, err)
	assert.True(t, called)

	for _, tc := range []struct {
		name    string
		res     launch.Resolution
		message string
	}{
		{"missing", launch.Missing, "ZCode is not installed"},
		{"inconclusive", launch.Unknown, "could not determine how to launch ZCode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registration.Locator = agenttest.AnsweringLocator(launch.Spec{}, tc.res)
			_, err := ResolveLaunch(context.Background(), agent.Options{Shell: "/bin/sh"}, registration)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}
