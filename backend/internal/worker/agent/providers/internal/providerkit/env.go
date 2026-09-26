package providerkit

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// agentIdentityEnvScrubKeys lists the environment variables that coding-agent harnesses
// set in the processes that they start, to mark the session identity, the nesting, the
// sandbox state, or distributed tracing. The harnesses are:
//   - Claude Code, Codex, Pi, Oh My Pi, Codewhale, MiMo Code, Amp and Cline.
//   - The ACP agents OpenCode, Kilo, Goose, Grok Build, Qwen Code and Kiro.
//
// A LeapMux worker can itself run inside one of these harnesses, for example when a
// developer runs the tests from a Claude Code, Codex or Pi terminal. The values then
// reach each agent that LeapMux starts, and that agent behaves as a nested session.
// LeapMux removes the whole union from each agent that it starts, so each one starts as
// a clean top-level session, whatever harness LeapMux ran under.
//
// Deliberately surgical -- only identity/session/nesting/sandbox/trace markers. These stay:
//   - Auth tokens: *_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, CODEX_API_KEY, AWS_*.
//   - Home and config dirs: CODEX_HOME, OPENCODE_CONFIG, PI_CODING_AGENT_DIR, GROK_HOME,
//     QWEN_HOME, MIMOCODE_HOME, CODEWHALE_HOME, KIRO_HOME, CLINE_DIR, CLINE_DATA_DIR, and the
//     GOOSE_MODEL/GOOSE_PROVIDER user overrides. The data variables of a Cline sandbox
//     are the exception: see sandboxDataEnvScrubs.
//   - Server addresses: AMP_URL, which points Amp at a server of the user's own.
//   - Provider-selection config: CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY.
//
// Each harness's rc-detection marker (CLAUDECODE, CODEX_CI, OPENCODE_CLIENT, KILO_CLIENT,
// MIMOCODE_CLIENT) and Claude's CLAUDE_CODE_ENTRYPOINT are intentionally
// NOT listed: each provider strips them from the inherited env, and a provider whose CLI
// needs one re-adds its own value, before this runs. Codex also strips CODEX_THREAD_ID in codex/start.go.
// This entry protects launches by other providers.
var agentIdentityEnvScrubKeys = []string{
	// Cross-harness: W3C distributed-trace context + generic "running as an agent" marker.
	"TRACEPARENT", "TRACESTATE", "AI_AGENT",
	// Claude Code (CLI child-env injector $tH + IDE/MCP SSE bridge + effort hard-override).
	// CLAUDE_CODE_SSE_PORT is the port an IDE/MCP-integrated session injects into its
	// terminal env; an inheriting child auto-connects to the PARENT's IDE bridge
	// (gated by `...||process.env.CLAUDE_CODE_SSE_PORT||...` in the CLI), so a worker
	// launched from an IDE terminal would otherwise spawn an agent wired to the parent.
	"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_EFFORT", "CLAUDE_PROJECT_DIR",
	"CLAUDE_CODE_SSE_PORT",
	// Codex (thread id, sandbox + network-proxy markers, rollout trace root).
	"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED",
	"CODEX_NETWORK_PROXY_ACTIVE", "CODEX_NETWORK_ALLOW_LOCAL_BINDING", "CODEX_ROLLOUT_TRACE_ROOT",
	// OpenCode (ACP) run identity.
	"OPENCODE_RUN_ID", "OPENCODE_PROCESS_ROLE", "_EXTENSION_OPENCODE_PORT",
	// Kilo (ACP) run identity (OpenCode fork).
	"KILO_RUN_ID", "KILO_PROCESS_ROLE",
	// Goose (ACP) -- injected session/terminal markers (no rc marker of its own).
	"GOOSE_TERMINAL", "AGENT_SESSION_ID",
	// Pi.
	"PI_CODING_AGENT",
	// Factory Droid -- identity and nesting markers its Task tool injects into
	// child processes (FACTORY_SESSION_PARENT_ID and the DROID_ spellings).
	"FACTORY_SESSION_PARENT_ID", "DROID_PARENT_SESSION_ID", "FACTORY_PROJECT_DIR",
	"CLAUDE_PROJECT_DIR", "DROID_PLUGIN_ROOT", "CLAUDE_PLUGIN_ROOT",
	// Letta Code -- identity and nesting markers its subagent launcher injects.
	"LETTA_AGENT_ID", "AGENT_ID", "LETTA_CONVERSATION_ID", "CONVERSATION_ID",
	"LETTA_PARENT_AGENT_ID", "LETTA_PARENT_CONVERSATION_ID", "LETTA_SUBAGENT_NAME",
	"LETTA_CODE_AGENT_ROLE", "LETTA_CODE_AGENT_TYPE", "LETTA_CODE_SUBAGENT_TAG",
	"LETTA_MEMORY_DIR", "MEMORY_DIR", "TRANSCRIPT_PATH",
	// Grok Build (ACP) -- the session id that it states to each command, MCP
	// server and hook that it runs.
	"GROK_SESSION_ID",
	// Qwen Code (ACP) -- the marker and the session context that it states to
	// each shell command that it runs (getShellContextEnvVars). A Qwen started
	// from such a command reads the session id, the project directory and the
	// model back from its own environment, so an inherited value puts it in the
	// parent's session.
	"QWEN_CODE", "QWEN_CODE_SESSION_ID", "QWEN_CODE_PROJECT_DIR", "QWEN_CODE_CLI",
	"QWEN_CODE_MODEL", "QWEN_CODE_MODEL_IDENTITY", "QWEN_CODE_AGENT_ID", "QWEN_CODE_PROMPT_ID",
	// Kiro (ACP) -- the root session id that its engine states to each shell
	// command and hook that it runs.
	"KIRO_SESSION_ID",
	// Oh My Pi. Its `bash` tool sets AGENT=1 on every command it runs
	// (exec/non-interactive-env.ts), and its eval kernels set PI_SESSION_FILE to the
	// PARENT session's file (eval/executor-base.ts). Either one, inherited, tells
	// the next agent that it runs inside an omp session.
	"AGENT", "PI_SESSION_FILE",
	// MiMo Code sets these in its own process environment at start, so every tool
	// command it runs inherits them. MIMOCODE marks a nested run, and a nested
	// MiMo reuses an inherited run id and process role instead of minting its own.
	"MIMOCODE", "MIMOCODE_PID", "MIMOCODE_RUN_ID", "MIMOCODE_PROCESS_ROLE",
	// Codewhale: the sandbox marker it sets on every command it runs, under its
	// current name and its legacy DeepSeek name, and the session id it gives a hook.
	"CODEWHALE_SANDBOX", "DEEPSEEK_SANDBOX", "CODEWHALE_SESSION_ID",
	// Amp sets AMP_CURRENT_THREAD_ID and AGENT_THREAD_ID on every tool command
	// that it runs, with AGENT=amp and AI_AGENT=amp, which the entries above
	// already scrub. AMP_THREAD_ID is the one that acts: Amp makes a new thread a
	// CHILD of the thread that it states. A worker started from a shell that
	// holds it would file every thread that it opens under that thread.
	"AMP_THREAD_ID", "AMP_CURRENT_THREAD_ID", "AGENT_THREAD_ID",
	// Cline sets these on itself, so each command that it runs inherits them.
	// CLINE_RUN_AS_HUB_DAEMON turns any `cline` into a hub daemon, and
	// CLINE_NO_INTERACTIVE marks a session that a daemon hosts. The npm wrapper
	// sets CLINE_WRAPPER_PATH, and every CLI process sets
	// CLINE_CONNECTOR_CLI_LAUNCH to its own launch command. The connector
	// supervisor marks the connectors that it starts, a resumed session marks its
	// hooks, and `--data-dir` marks a sandboxed run. CLINE_DIR, CLINE_DATA_DIR and
	// the other directory variables stay, because they locate the user's own
	// Cline configuration. The one exception is a sandboxed run, which
	// sandboxDataEnvScrubs covers.
	"CLINE_RUN_AS_HUB_DAEMON", "CLINE_NO_INTERACTIVE", "CLINE_WRAPPER_PATH",
	"CLINE_CONNECTOR_CLI_LAUNCH", "CLINE_CONNECTOR_STARTING_INSTANCE", "CLINE_CONNECTOR_SUPERVISED",
	"CLINE_HOOK_AGENT_RESUME", "CLINE_SANDBOX", "CLINE_SANDBOX_DATA_DIR",
}

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
// vars (see agentIdentityEnvScrubKeys) so a worker launched from inside
// another agent's session doesn't spawn a nested one, strips the data
// variables of an inherited sandbox (see sandboxDataEnvScrubs), strips any
// inherited `LEAPMUX_CONTROL_*` values so a worker spawned inside another
// worker's session never inherits the parent's remote context (any
// fresh values arrive via opts.ExtraEnv), strips an inherited
// provider-helper variable, PINS the `LEAPMUX_WORKER=1`
// marker (downstream CLI/agent code keys off it to detect "running
// inside a LeapMux worker") and git's optional-lock setting, and appends
// `opts.ExtraEnv`.
//
// Pins, not appends: a worker launched from inside a LeapMux terminal or
// agent already carries both values, so appending would hand the child
// two entries for each -- see envutil.PinEnv. opts.ExtraEnv still lands
// last, after the pins, because that is where the caller's authoritative
// values belong.
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
	// Both scrubs must run before the ExtraEnv append so opts.ExtraEnv's
	// fresh LEAPMUX_CONTROL_* values aren't stripped. The sandbox scrub runs
	// first, because it reads the sandbox marker that the identity scrub removes.
	env = scrubSandboxData(env)
	env = envutil.FilterEnv(env, agentIdentityEnvScrubKeys...)
	env = envutil.StripByPrefix(env, "LEAPMUX_CONTROL_")
	// The provider-helper variable is LeapMux's own (see agent.HelperFunc). An
	// inherited copy points at ANOTHER agent's helper, so it never passes. A
	// provider whose CLI starts a helper adds its own value after this call.
	env = envutil.FilterEnv(env, contracts.EnvAgentHelper)
	// PinEnv, not append: every caller hands us an inherited environment, and a
	// worker launched from a LeapMux terminal or agent already carries both of
	// these -- so appending would layer a second entry rather than replace the
	// inherited one. See envutil.PinEnv.
	env = envutil.PinEnv(env, "LEAPMUX_WORKER=1", gitutil.GitOptionalLocksOff)
	if len(opts.ExtraEnv) == 0 {
		return env
	}
	return append(env, opts.ExtraEnv...)
}
