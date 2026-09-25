package bootstrap

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"
)

// RunAgentHelper runs the provider helper that the spec at specPath states, and
// returns the exit code for the process. It dispatches through the same
// registry that Wire builds, so an executable reaches the providers through
// this package alone, as the worker does. See agent.HelperFunc for what a
// helper is.
func RunAgentHelper(ctx context.Context, specPath string, invocation agent.HelperInvocation) int {
	return providers.Registry().RunHelper(ctx, specPath, invocation)
}
