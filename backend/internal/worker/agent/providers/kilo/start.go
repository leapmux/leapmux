package kilo

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// kiloQuestionToolEnv is Kilo's spelling of openCodeQuestionToolEnv: the flag
// that turns ON the daemon's own question tool, which the question bridge in
// opencode/questions.go answers. Kilo keeps OpenCode's gate, so the reason to pin
// it is the same.
const kiloQuestionToolEnv = "KILO_ENABLE_QUESTION_TOOL"

var _ agent.StartFunc = Start

// Start starts a Kilo ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return opencode.StartFamily(ctx, opts, sink, Registration(), opencode.FamilySpec{
		ProviderName:   "kilo",
		RCMarkerEnvKey: "KILO_CLIENT",
		// Kilo forks OpenCode and requires its own spelling of the flag for the same
		// tool; see kiloQuestionToolEnv. Its allowed client list adds `vscode`, and
		// its own `kilo acp` handler assigns `KILO_CLIENT="acp"` the same way, so the
		// flag is the only route here too.
		QuestionToolEnv:     kiloQuestionToolEnv,
		DefaultPrimaryAgent: PrimaryAgentCode,
	}, func() *Agent { return &Agent{} }, func(a *Agent) *opencode.FamilyBase { return &a.FamilyBase })
}
