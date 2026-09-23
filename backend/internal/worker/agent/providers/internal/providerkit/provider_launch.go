package providerkit

import (
	"context"

	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ResolveLaunch resolves how to start a provider's program for one launch.
//
// The registration supplies the locator and display name for both launch and
// availability checks. The two paths therefore select the same program.
func ResolveLaunch(ctx context.Context, opts agent.Options, registration agent.Registration) (launch.Spec, error) {
	return registration.Locator.Resolve(ctx, opts.Shell, opts.LoginShell, agentlabels.DisplayName(registration.Provider))
}
