// Package agentenv holds the single source of truth for coding-agent harness
// environment variables shared across the Worker and the control CLI:
//   - HarnessDetectionMarkers identifies processes running inside a coding-agent
//     harness so CLI commands avoid disruptive interactive behavior such as
//     stealing window focus.
//   - IdentityScrubKeys lists the environment variables that the Worker removes
//     from spawned child agents so an agent started from a Worker running inside
//     another harness does not inherit a parent session, sandbox, or trace.
//
// Both slices derive from one set of harness-specific session markers so new
// harnesses stay aligned across detection and scrubbing.
package agentenv

import "slices"

// primaryDetectionMarkers lists unambiguous coding-agent harness markers that
// HarnessDetectionMarkers checks, but IdentityScrubKeys intentionally omits.
//
// Each provider handles its own rc-detection or entrypoint variable
// (CLAUDECODE, CLAUDE_CODE_ENTRYPOINT, CODEX_CI, OPENCODE_CLIENT, KILO_CLIENT,
// MIMOCODE_CLIENT): the provider strips the inherited copy and re-adds its own
// value before FinalizeAgentEnv runs. The remaining entries (OPENCODE,
// GEMINI_CLI, ANTIGRAVITY_AGENT, CURSOR_AGENT, AIDER) are harness-detection
// markers that FinalizeAgentEnv does not scrub.
var primaryDetectionMarkers = []string{
	// Claude Code rc-detection and entrypoint markers.
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
	// Codex rc-detection marker.
	"CODEX_CI",
	// OpenCode harness and rc-detection markers.
	"OPENCODE", "OPENCODE_CLIENT",
	// Kilo rc-detection marker.
	"KILO_CLIENT",
	// MiMo Code rc-detection marker.
	"MIMOCODE_CLIENT",
	// Other coding-agent harnesses.
	"GEMINI_CLI", "ANTIGRAVITY_AGENT", "CURSOR_AGENT", "AIDER",
}

// sharedHarnessSessionMarkers lists unambiguous harness-specific session,
// nesting, and sandbox markers that coding-agent harnesses inject into child
// processes. Every key here belongs to both HarnessDetectionMarkers and
// IdentityScrubKeys.
var sharedHarnessSessionMarkers = []string{
	// Claude Code (CLI child-env injector + IDE/MCP SSE bridge + plugin root).
	// CLAUDE_CODE_SSE_PORT is the port an IDE/MCP-integrated session injects
	// into its terminal env; an inheriting child auto-connects to the parent's
	// IDE bridge (controlled by `...||process.env.CLAUDE_CODE_SSE_PORT||...` in
	// the CLI), so a worker launched from an IDE terminal would otherwise spawn
	// an agent wired to the parent.
	"CLAUDE_CODE_SESSION_ID", "CLAUDE_PROJECT_DIR", "CLAUDE_CODE_SSE_PORT", "CLAUDE_PLUGIN_ROOT",
	// Codex (thread id, sandbox + network-proxy markers, rollout trace root).
	// Codex also strips CODEX_THREAD_ID in codex/start.go; keeping it here
	// protects launches by other providers.
	"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED",
	"CODEX_NETWORK_PROXY_ACTIVE", "CODEX_NETWORK_ALLOW_LOCAL_BINDING", "CODEX_ROLLOUT_TRACE_ROOT",
	// OpenCode run identity.
	"OPENCODE_RUN_ID", "OPENCODE_PROCESS_ROLE", "_EXTENSION_OPENCODE_PORT",
	// Kilo (ACP) run identity (OpenCode fork).
	"KILO_RUN_ID", "KILO_PROCESS_ROLE",
	// Goose (ACP) injected terminal marker.
	"GOOSE_TERMINAL",
	// Pi and Oh My Pi. Oh My Pi eval kernels set PI_SESSION_FILE to the parent
	// session's file (eval/executor-base.ts), which tells the next agent that
	// it runs inside an omp session.
	"PI_CODING_AGENT", "PI_SESSION_FILE",
	// Factory Droid identity and nesting markers its Task tool injects into
	// child processes (FACTORY_SESSION_PARENT_ID and the DROID_ spellings).
	"FACTORY_SESSION_PARENT_ID", "DROID_PARENT_SESSION_ID", "FACTORY_PROJECT_DIR", "DROID_PLUGIN_ROOT",
	// Letta Code prefixed identity and nesting markers its subagent launcher injects.
	"LETTA_AGENT_ID", "LETTA_CONVERSATION_ID",
	"LETTA_PARENT_AGENT_ID", "LETTA_PARENT_CONVERSATION_ID", "LETTA_SUBAGENT_NAME",
	"LETTA_CODE_AGENT_ROLE", "LETTA_CODE_AGENT_TYPE", "LETTA_CODE_SUBAGENT_TAG",
	"LETTA_MEMORY_DIR",
	// Grok Build (ACP) session id given to each command, MCP server, and hook.
	"GROK_SESSION_ID",
	// Qwen Code (ACP) marker and shell context given to each shell command
	// (getShellContextEnvVars). A Qwen started from such a command reads the
	// session id, project directory, and model back from its own environment, so
	// an inherited value puts it in the parent's session.
	"QWEN_CODE", "QWEN_CODE_SESSION_ID", "QWEN_CODE_PROJECT_DIR", "QWEN_CODE_CLI",
	"QWEN_CODE_MODEL", "QWEN_CODE_MODEL_IDENTITY", "QWEN_CODE_AGENT_ID", "QWEN_CODE_PROMPT_ID",
	// Kiro (ACP) root session id given to each shell command and hook.
	"KIRO_SESSION_ID",
	// MiMo Code sets these on itself at start, so every tool command inherits
	// them. MIMOCODE marks a nested run, and a nested MiMo reuses an inherited
	// run id and process role instead of minting its own.
	"MIMOCODE", "MIMOCODE_PID", "MIMOCODE_RUN_ID", "MIMOCODE_PROCESS_ROLE",
	// Codewhale sandbox markers (current and legacy DeepSeek spelling) and hook
	// session id.
	"CODEWHALE_SANDBOX", "DEEPSEEK_SANDBOX", "CODEWHALE_SESSION_ID",
	// Amp thread markers. AMP_THREAD_ID makes a new thread a child of the thread
	// that it specifies, so a worker started from a shell that holds it would
	// file every new thread under that parent thread.
	"AMP_THREAD_ID", "AMP_CURRENT_THREAD_ID",
	// Cline process and sandbox markers. CLINE_RUN_AS_HUB_DAEMON turns any
	// `cline` into a hub daemon, CLINE_NO_INTERACTIVE marks a daemon-hosted
	// session, CLINE_WRAPPER_PATH comes from the npm wrapper,
	// CLINE_CONNECTOR_CLI_LAUNCH holds the CLI launch command, the connector
	// supervisor marks its connectors, a resumed session marks its hooks, and
	// `--data-dir` marks a sandboxed run. User config directories (CLINE_DIR,
	// CLINE_DATA_DIR, etc.) stay unless a sandbox marker turns on conditional
	// scrubbing in providerkit.
	"CLINE_RUN_AS_HUB_DAEMON", "CLINE_NO_INTERACTIVE", "CLINE_WRAPPER_PATH",
	"CLINE_CONNECTOR_CLI_LAUNCH", "CLINE_CONNECTOR_STARTING_INSTANCE", "CLINE_CONNECTOR_SUPERVISED",
	"CLINE_HOOK_AGENT_RESUME", "CLINE_SANDBOX", "CLINE_SANDBOX_DATA_DIR",
}

// scrubOnlyKeys lists generic trace, ambient, subagent, and effort-override
// variables that IdentityScrubKeys must remove from spawned child agents, but
// HarnessDetectionMarkers omits because they can appear in an ordinary shell.
var scrubOnlyKeys = []string{
	// Cross-harness W3C distributed-trace context and generic agent markers.
	// Oh My Pi sets AGENT=1 on bash commands; Amp sets AGENT=amp and AI_AGENT=amp;
	// Goose injects AGENT_SESSION_ID; Amp injects AGENT_THREAD_ID.
	"TRACEPARENT", "TRACESTATE", "AI_AGENT", "AGENT", "AGENT_SESSION_ID", "AGENT_THREAD_ID",
	// Claude Code effort overrides (a user can also export these in a shell
	// profile, so they do not prove the shell runs inside Claude Code).
	"CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_EFFORT",
	// Unprefixed Letta Code subagent variables that are too generic for shell
	// harness detection.
	"AGENT_ID", "CONVERSATION_ID", "MEMORY_DIR", "TRANSCRIPT_PATH",
}

// HarnessDetectionMarkers lists unambiguous coding-agent environment variables
// that indicate the current process runs inside a coding-agent harness.
//
// Callers must treat this slice as read-only. It excludes generic trace,
// memory, transcript, and CI variables (such as TRACEPARENT, CONVERSATION_ID,
// MEMORY_DIR, TRANSCRIPT_PATH, AGENT_ID, and CI) that can appear in ordinary
// shells or non-agent automation.
var HarnessDetectionMarkers = slices.Concat(primaryDetectionMarkers, sharedHarnessSessionMarkers)

// IdentityScrubKeys lists the exact environment variables that the Worker
// strips from spawned child agents so each agent starts as a clean top-level
// session regardless of which harness launched the Worker.
//
// Callers must treat this slice as read-only. It is deliberately surgical and
// preserves:
//   - Auth tokens: *_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, CODEX_API_KEY, AWS_*.
//   - Home and config dirs: CODEX_HOME, OPENCODE_CONFIG, PI_CODING_AGENT_DIR,
//     GROK_HOME, QWEN_HOME, MIMOCODE_HOME, CODEWHALE_HOME, KIRO_HOME,
//     CLINE_DIR, CLINE_DATA_DIR, and GOOSE_MODEL/GOOSE_PROVIDER user overrides.
//   - Server addresses: AMP_URL.
//   - Provider-selection config: CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY.
//   - Primary rc-detection and entrypoint markers: CLAUDECODE, CODEX_CI,
//     OPENCODE_CLIENT, KILO_CLIENT, MIMOCODE_CLIENT, CLAUDE_CODE_ENTRYPOINT.
var IdentityScrubKeys = slices.Concat(scrubOnlyKeys, sharedHarnessSessionMarkers)
