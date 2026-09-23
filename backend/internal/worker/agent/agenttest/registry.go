package agenttest

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// MustNewRegistry builds a registry for a test from registrations that are
// valid by construction, and panics otherwise.
func MustNewRegistry(regs ...agent.Registration) *agent.Registry {
	r, err := agent.NewRegistry(regs...)
	if err != nil {
		panic(err)
	}
	return r
}

// AnsweringLocator is a locator whose resolver answers spec and res without
// probing anything.
func AnsweringLocator(spec launch.Spec, res launch.Resolution) launch.Locator {
	return launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return spec, res
	})
}
