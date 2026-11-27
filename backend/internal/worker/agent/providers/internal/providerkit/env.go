package providerkit

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/agentenv"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// sandboxDataEnv identifies a harness sandbox by its marker, and lists the data
// variables that the sandbox sets on itself beside that marker.
type sandboxDataEnv struct {
	// marker is the variable that turns the sandbox on.
	marker string
	// on is the value of marker, with the spaces at each end removed, that
	// turns the sandbox on.
	on string
	// dataKeys are the variables that the sandbox points at its own data.
	dataKeys []string
}

// sandboxDataEnvScrubs lists the sandboxes whose data variables a scrub removes
// only when the sandbox marker is present. Without the marker, the same
// variables are the user's own configuration, so they stay.
var sandboxDataEnvScrubs = []sandboxDataEnv{
	// `cline --data-dir`, or an inherited CLINE_SANDBOX=1, runs Cline in a
	// sandbox. Cline then sets CLINE_SANDBOX=1 and points these variables at
	// the sandbox directory (configureSandboxEnvironment in Cline 3.0.64), so
	// each command that it runs inherits them. A worker started from such a
	// command would otherwise drive the sandbox's data and not the user's.
	{
		marker: "CLINE_SANDBOX",
		on:     "1",
		dataKeys: []string{
			"CLINE_DATA_DIR", "CLINE_DB_DATA_DIR", "CLINE_SESSION_DATA_DIR", "CLINE_TEAM_DATA_DIR",
			"CLINE_PROVIDER_SETTINGS_PATH", "CLINE_HOOKS_LOG_PATH",
		},
	},
}

// scrubSandboxData removes the data variables of each sandbox whose marker env
// turns on. It reads the marker before the identity scrub removes it.
func scrubSandboxData(env []string) []string {
	for _, sandbox := range sandboxDataEnvScrubs {
		if slices.ContainsFunc(envutil.ValuesFor(env, sandbox.marker), func(value string) bool {
			return strings.TrimSpace(value) == sandbox.on
		}) {
			env = envutil.FilterEnv(env, sandbox.dataKeys...)
		}
	}
	return env
}

// FinalizeAgentEnv applies the env-mutations every spawned agent
// process needs in one place: strips inherited agent-harness identity
// vars (see agentenv.IdentityScrubKeys) so a worker launched from inside
// another agent's session doesn't spawn a nested one, strips the data
// variables of an inherited sandbox (see sandboxDataEnvScrubs), strips an
// inherited provider-helper variable, PINS the `LEAPMUX_WORKER=1`
// marker (downstream CLI/agent code keys off it to detect "running
// inside a LeapMux worker") and git's optional-lock setting, and applies
// `opts.ExtraEnv` through controlenv.Apply. That last step strips any
// inherited `LEAPMUX_CONTROL_*` values, so a worker spawned inside another
// worker's session never inherits the parent's remote context, appends the
// fresh values, and puts the directory of the Worker's CLI first on PATH.
//
// Pins, not appends: a worker launched from inside a LeapMux terminal or
// agent already carries both values, so appending would hand the child
// two entries for each -- see envutil.PinEnv. opts.ExtraEnv still lands
// last, after the pins, because that is where the caller's authoritative
// values belong.
//
// An agent that its provider starts through a login shell gets the PATH entry
// before the shell's profile runs, and the profile can move it or drop it. The
// command that launch.Wrap builds puts it first again after the profile, from
// the LEAPMUX_CONTROL_CLI that this function sets. See controlenv.Apply.
//
// It also declines git's OPTIONAL index lock for everything the agent runs --
// see gitutil.GitOptionalLocksOff for why. The worker set that on its own git
// commands, which removed only ONE of the contenders: a coding agent polls
// `git status` continuously, and that probe takes .git/index.lock purely to
// write back a refreshed index, killing a concurrent worker checkout with
// "Another git process seems to be running". The agent's own mutations are
// unaffected -- their index lock is required, not optional.
//
// Provider-specific env additions (CLAUDE_CODE_ENTRYPOINT, CODEX_CI,
// etc.) go BEFORE this call so they survive both the identity scrub and
// the LEAPMUX_CONTROL_* strip and stack with the marker.
func FinalizeAgentEnv(env []string, opts agent.Options) []string {
	// Both scrubs must run before controlenv.Apply appends opts.ExtraEnv, so
	// they cannot remove a fresh value. The sandbox scrub runs first, because it
	// reads the sandbox marker that the identity scrub removes.
	env = scrubSandboxData(env)
	env = envutil.FilterEnv(env, agentenv.IdentityScrubKeys...)
	// The provider-helper variable is LeapMux's own (see agent.HelperFunc). An
	// inherited copy points at ANOTHER agent's helper, so it never passes. A
	// provider whose CLI starts a helper adds its own value after this call.
	env = envutil.FilterEnv(env, contracts.EnvAgentHelper)
	// PinEnv, not append: every caller hands us an inherited environment, and a
	// worker launched from a LeapMux terminal or agent already carries both of
	// these -- so appending would layer a second entry rather than replace the
	// inherited one. See envutil.PinEnv.
	env = envutil.PinEnv(env, "LEAPMUX_WORKER=1", gitutil.GitOptionalLocksOff)
	return controlenv.Apply(env, opts.ExtraEnv)
}
