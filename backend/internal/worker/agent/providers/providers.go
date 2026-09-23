// Package providers is the composition root of the agent providers. It states,
// in one explicit list, every provider this worker supports, and builds the
// agent.Registry the worker runs on from that list.
//
// The list is explicit on purpose. A provider package that registered itself
// from init() would be wired by a blank import somewhere else, and a missing
// import would make every lookup for that provider quietly answer the neutral
// defaults. Here a provider missing from the list is a startup panic, and
// providers_test.go fails first.
package providers

import (
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/copilot"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cursor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/goose"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/pi"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/reasonix"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/zcode"
)

// Registrations returns the Registration of every provider this worker
// supports, in enum order. A caller that needs a variant registry -- a test that
// wraps one provider's plugin -- starts from this list.
func Registrations() []agent.Registration {
	return []agent.Registration{
		claude.Registration(),
		codex.Registration(),
		cursor.Registration(),
		copilot.Registration(),
		kilo.Registration(),
		opencode.Registration(),
		goose.Registration(),
		pi.Registration(),
		reasonix.Registration(),
		zcode.Registration(),
	}
}

// Registry builds the registry of every provider this worker supports.
//
// It panics when the list cannot build a registry, or when an AgentProvider
// value has no registration. Both are wiring mistakes in this package that no
// input can cause, and the worker must not start without a provider it claims
// to support.
func Registry() *agent.Registry {
	r, err := agent.NewRegistry(Registrations()...)
	if err != nil {
		panic(fmt.Sprintf("providers: invalid registration: %v", err))
	}
	if missing := unregistered(r); len(missing) > 0 {
		panic(fmt.Sprintf("providers: no registration for %v", missing))
	}
	return r
}

// unregistered returns every AgentProvider value, UNSPECIFIED excepted, that r
// has no registration for, in enum order.
func unregistered(r *agent.Registry) []leapmuxv1.AgentProvider {
	registered := r.Providers()
	var missing []leapmuxv1.AgentProvider
	for value := range leapmuxv1.AgentProvider_name {
		p := leapmuxv1.AgentProvider(value)
		if p == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED || slices.Contains(registered, p) {
			continue
		}
		missing = append(missing, p)
	}
	slices.Sort(missing)
	return missing
}
