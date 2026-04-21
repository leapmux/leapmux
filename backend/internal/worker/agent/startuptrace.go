package agent

import (
	"log/slog"
	"os"
)

// startupTraceEnabled is set at process start from the
// LEAPMUX_TRACE_AGENT_STARTUP env var. When enabled, TraceStartupPhase
// emits a slog.Info line tagged with marker=agent_startup_timing so that
// tests (e.g. the agent-startup-timing e2e) can parse the phase timeline
// from stderr. Off by default — production logs are unaffected.
var startupTraceEnabled = os.Getenv("LEAPMUX_TRACE_AGENT_STARTUP") == "1"

// TraceStartupPhase emits a structured timing log line for the given agent
// startup phase. No-op unless LEAPMUX_TRACE_AGENT_STARTUP=1.
//
// Time is taken from the log handler at write time; the receiving test
// correlates lines by agent_id and the phase name.
func TraceStartupPhase(agentID, phase string) {
	if !startupTraceEnabled {
		return
	}
	slog.Info("agent startup timing", "marker", "agent_startup_timing", "agent_id", agentID, "phase", phase)
}
