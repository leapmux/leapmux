package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"

	"github.com/stretchr/testify/require"
)

// testRegistry is the registry of every provider the worker supports: the one
// bootstrap wires into production. A test that needs a variant -- one
// provider's plugin wrapped -- builds its own from providers.Registrations()
// and passes it through withRegistry.
var testRegistry = providers.Registry()

func registryWithRegistration(t *testing.T, provider leapmuxv1.AgentProvider, configure func(*agent.Registration)) *agent.Registry {
	t.Helper()
	registrations := providers.Registrations()
	found := false
	for index := range registrations {
		if registrations[index].Provider == provider {
			configure(&registrations[index])
			found = true
		}
	}
	require.Truef(t, found, "%v has no registration to replace", provider)
	registry, err := agent.NewRegistry(registrations...)
	require.NoError(t, err)
	return registry
}

func registryWithQueueReconciliation(t *testing.T, provider leapmuxv1.AgentProvider) *agent.Registry {
	t.Helper()
	return registryWithRegistration(t, provider, func(registration *agent.Registration) {
		registration.ReconcileBeforeQueueDispatch = true
	})
}

type queueReconciliationAgent struct {
	agent.Agent
	reconcile func(context.Context) error
}

func (runtime *queueReconciliationAgent) ReconcileBeforeDispatch(ctx context.Context) error {
	return runtime.reconcile(ctx)
}

// registryWithPlugin returns the registry of every provider, with provider's
// wire-format plugin replaced by plugin. Only the test that builds it sees the
// replacement, so the test needs no exclusion from parallel runs.
func registryWithPlugin(t *testing.T, provider leapmuxv1.AgentProvider, plugin agent.Provider) *agent.Registry {
	t.Helper()
	return registryWithRegistration(t, provider, func(registration *agent.Registration) { registration.Plugin = plugin })
}

// startWith returns a start function of the shape the service holds in
// startAgentFn: it registers the agent in m through the start function the
// test chose, so a test can stand a mock process in for the provider.
func startWith(m *agent.Manager, start agent.StartFunc) func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
	return func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return m.StartAgentWith(ctx, opts, sink, start)
	}
}
