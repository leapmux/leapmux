package agent

import "github.com/leapmux/leapmux/internal/util/envutil"

// agentIdentityEnvScrubKeys are environment variables that coding-agent harnesses
// (Claude Code, Codex, OpenCode/Kilo/Goose ACP agents, Pi) inject into the processes they
// spawn to mark session identity, nesting, sandbox state, or distributed tracing. When a
// LeapMux worker is itself launched from inside one of these harnesses (e.g. a developer
// running the test suite from their Claude Code / Codex / Pi terminal), these leak into the
// agent LeapMux spawns and make it behave as a nested session. We strip the whole union from
// every spawned agent so each starts as a clean, top-level session, regardless of which
// harness LeapMux ran under.
//
// Deliberately surgical -- only identity/session/nesting/sandbox/trace markers. Auth tokens
// (*_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, CODEX_API_KEY, AWS_*), home/config dirs (CODEX_HOME,
// OPENCODE_CONFIG, PI_CODING_AGENT_DIR, GOOSE_MODEL/GOOSE_PROVIDER user overrides), and
// provider-selection config (CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY) are preserved.
//
// Each harness's rc-detection marker (CLAUDECODE, CODEX_CI, OPENCODE_CLIENT, KILO_CLIENT,
// GEMINI_CLI/GEMINI_CLI_NO_RELAUNCH) and Claude's CLAUDE_CODE_ENTRYPOINT are intentionally
// NOT listed: each provider strips them from the inherited env and re-adds its own value
// before this runs. CODEX_THREAD_ID is also stripped in codex.go; listing it here too is a
// harmless redundant strip that also protects non-codex launches.
var agentIdentityEnvScrubKeys = []string{
	// Cross-harness: W3C distributed-trace context + generic "running as an agent" marker.
	"TRACEPARENT", "TRACESTATE", "AI_AGENT",
	// Claude Code (CLI child-env injector $tH + MCP spawn + effort hard-override).
	"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_EFFORT", "CLAUDE_PROJECT_DIR",
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
}

// FinalizeAgentEnv applies the env-mutations every spawned agent
// process needs in one place: strips inherited agent-harness identity
// vars (see agentIdentityEnvScrubKeys) so a worker launched from inside
// another agent's session doesn't spawn a nested one, strips any
// inherited `LEAPMUX_REMOTE_*` values so a worker spawned inside another
// worker's session never inherits the parent's remote context (any
// fresh values arrive via opts.ExtraEnv), appends the `LEAPMUX_WORKER=1`
// marker (downstream CLI/agent code keys off it to detect "running
// inside a LeapMux worker"), and appends `opts.ExtraEnv`.
//
// Provider-specific env additions (CLAUDE_CODE_ENTRYPOINT, CODEX_CI,
// etc.) go BEFORE this call so they survive both the identity scrub and
// the LEAPMUX_REMOTE_* strip and stack with the marker.
func FinalizeAgentEnv(env []string, opts Options) []string {
	// Both scrubs must run before the ExtraEnv append so opts.ExtraEnv's
	// fresh LEAPMUX_REMOTE_* values aren't stripped.
	env = envutil.FilterEnv(env, agentIdentityEnvScrubKeys...)
	env = envutil.StripByPrefix(env, "LEAPMUX_REMOTE_")
	env = append(env, "LEAPMUX_WORKER=1")
	if len(opts.ExtraEnv) == 0 {
		return env
	}
	return append(env, opts.ExtraEnv...)
}
