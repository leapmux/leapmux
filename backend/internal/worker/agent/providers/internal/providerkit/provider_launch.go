package providerkit

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ResolveLaunch resolves how to start a provider's program for one launch.
//
// Every provider passes the same locator its Registration carries, so the
// launch and the availability scan can never disagree about which program a
// provider runs. The provider's display name identifies it in the error a user
// sees when the program cannot be found.
func ResolveLaunch(ctx context.Context, opts agent.Options, provider leapmuxv1.AgentProvider, locator launch.Locator) (launch.Spec, error) {
	return locator.Resolve(ctx, opts.Shell, opts.LoginShell, agentlabels.DisplayName(provider))
}
