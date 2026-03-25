package agent

import (
	"context"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		func(_ context.Context, _ Options, _ OutputSink) (Provider, error) {
			return nil, fmt.Errorf("opencode provider is not implemented yet")
		},
		nil,
		nil,
		"LEAPMUX_OPENCODE_DEFAULT_MODEL",
		"LEAPMUX_OPENCODE_DEFAULT_EFFORT",
		"opencode",
	)
}
