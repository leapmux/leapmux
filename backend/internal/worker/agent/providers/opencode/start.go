package opencode

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

var _ agent.StartFunc = Start

// Start starts an OpenCode ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return StartFamily(ctx, opts, sink, Registration(), FamilySpec{
		ProviderName:        "opencode",
		RCMarkerEnvKey:      "OPENCODE_CLIENT",
		QuestionToolEnv:     openCodeQuestionToolEnv,
		DefaultPrimaryAgent: PrimaryAgentBuild,
	}, func() *Agent { return &Agent{} }, func(a *Agent) *FamilyBase { return &a.FamilyBase })
}
