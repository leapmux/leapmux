// Package providers explicitly registers every provider that the Worker supports.
// It constructs the single agent.Registry from those registrations.
// A missing registration fails before the Worker starts.
package providers

import (
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/amp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cline"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codebuddy"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codewhale"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/commandcode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/copilot"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/cursor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/deepseekharness"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/dirac"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/droid"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/fastagent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/gemini"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/goose"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/grok"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/junie"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kimi"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kiro"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/letta"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/mimo"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/muse"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/ohmypi"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/pi"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/qoder"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/qwen"
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
		codewhale.Registration(),
		kimi.Registration(),
		mimo.Registration(),
		qwen.Registration(),
		ohmypi.Registration(),
		grok.Registration(),
		kiro.Registration(),
		amp.Registration(),
		cline.Registration(),
		codebuddy.Registration(),
		junie.Registration(),
		letta.Registration(),
		dirac.Registration(),
		qoder.Registration(),
		droid.Registration(),
		fastagent.Registration(),
		commandcode.Registration(),
		deepseekharness.Registration(),
		gemini.Registration(),
		muse.Registration(),
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
