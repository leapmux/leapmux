package opencode

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// openCodeDisableAutoUpdateEnv turns OFF the upgrade of OpenCode's own install
// (curl, npm, brew and the other methods) for a truthy value, `true` or `1`.
// OpenCode runs the upgrade from the check that its TUI starts one second after
// it opens, and never from `opencode acp`; the `autoupdate` key of an inline
// OPENCODE_CONFIG_CONTENT cannot carry the switch, because the check reads only
// the global config files. Kilo has its own spelling; see kiloDisableAutoUpdateEnv.
const openCodeDisableAutoUpdateEnv = "OPENCODE_DISABLE_AUTOUPDATE"

var _ agent.StartFunc = Start

// Start starts an OpenCode ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return StartFamily(ctx, opts, sink, Registration(), FamilySpec{
		ProviderName:         "opencode",
		RCMarkerEnvKey:       "OPENCODE_CLIENT",
		QuestionToolEnv:      openCodeQuestionToolEnv,
		DisableAutoUpdateEnv: openCodeDisableAutoUpdateEnv,
		DefaultPrimaryAgent:  PrimaryAgentBuild,
	}, func() *Agent { return &Agent{} }, func(a *Agent) *FamilyBase { return &a.FamilyBase })
}
