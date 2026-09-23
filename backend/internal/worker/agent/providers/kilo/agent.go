package kilo

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

const PrimaryAgentCode = "code"

// Agent manages a single Kilo ACP process.
type Agent struct {
	opencode.FamilyBase
}

// kiloQuestionToolEnv is Kilo's spelling of openCodeQuestionToolEnv: the flag
// that turns ON the daemon's own question tool, which the question bridge in
// opencode/questions.go answers. Kilo keeps OpenCode's gate, so the reason to pin
// it is the same.
const kiloQuestionToolEnv = "KILO_ENABLE_QUESTION_TOOL"

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

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentCode, Name: providerkit.TitleCaseID(PrimaryAgentCode, "")},
		{Id: opencode.PrimaryAgentPlan, Name: providerkit.TitleCaseID(opencode.PrimaryAgentPlan, "")},
	}
}

// kiloStaticOptionGroups holds Kilo's static primary-agent group. Registration
// gives this group to both the registry and the ACP start.
var kiloStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPrimaryAgent, fallbackKiloPrimaryAgents())

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Agent steers through the family base, which it embeds, so a fork cannot lose
// the capability by not restating it. This assertion makes that regression a
// compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// kiloLocator finds the Kilo CLI on the user's PATH.
var kiloLocator = launch.Binaries("kilo")

// Registration states everything the worker knows about Kilo before any of
// its agents runs. Kilo shares OpenCode's registration shape.
func Registration() agent.Registration {
	return opencode.FamilyRegistration(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		kiloProvider{},
		Start,
		kiloLocator,
		kiloStaticOptionGroups,
		"LEAPMUX_KILO_DEFAULT_MODEL", "LEAPMUX_KILO_DEFAULT_EFFORT",
	)
}
