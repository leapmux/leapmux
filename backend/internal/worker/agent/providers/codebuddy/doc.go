// Package codebuddy drives CodeBuddy Code, Tencent's terminal coding agent,
// over its documented stream-json stdio protocol.
//
// CodeBuddy's stream is deliberately Claude Code 2.1.220-shaped, and its own
// docs promise byte-level compatibility for workflow events. The package still
// shares no code with providers/claude, because the shapes LeapMux parses
// differ where it matters most. The one hard break is the can_use_tool answer:
// Claude wants {"behavior":"allow"}, CodeBuddy wants {"allowed":true}. A shared
// implementation would silently deny every tool call.
//
// The worker runs one long-lived `codebuddy` process per agent:
//
//	codebuddy -p --input-format stream-json --output-format stream-json
//
// Each stdin line is one NDJSON user or control_request frame. Each stdout line
// is one NDJSON system, assistant, user, result, control_request,
// control_response or tool_progress frame. The process stays up across turns,
// so a `result` frame ends a turn and not the process.
//
// Isolation for a LeapMux agent is CODEBUDDY_CONFIG_DIR plus a per-agent HOME,
// with the telemetry and updater collectors turned off. See
// frontend/tests/e2e/helpers/mockAgentEnvironment.ts for the E2E recipe and
// start.go for the production environment.
package codebuddy
