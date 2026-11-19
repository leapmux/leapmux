package providerkit

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ResolveLaunch resolves how to start a provider's program for one launch.
//
// Discovery and launch use the locator supplied by the provider registration.
func ResolveLaunch(ctx context.Context, opts agent.Options, registration agent.Registration) (launch.Spec, error) {
	probeCtx, cancel := context.WithTimeout(ctx, opts.EffectiveStartupTimeout())
	defer cancel()

	provider := agentlabels.DisplayName(registration.Provider)
	spec, err := registration.Locator.Resolve(probeCtx, opts.Shell, opts.LoginShell, provider)
	if probeErr := probeCtx.Err(); probeErr != nil {
		return launch.Spec{}, fmt.Errorf("resolve %s launch: %w", provider, probeErr)
	}
	return spec, err
}
