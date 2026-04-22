package agent

import "github.com/leapmux/leapmux/internal/util/phasetrace"

// startupTracer emits structured timing log lines tagged with
// marker=agent_startup_timing when LEAPMUX_TRACE_AGENT_STARTUP=1, so
// that tests (e.g. the agent-startup-timing e2e) can parse the phase
// timeline from stderr. Off by default — production logs are unaffected.
var startupTracer = phasetrace.New(
	"LEAPMUX_TRACE_AGENT_STARTUP",
	"agent_startup_timing",
	"agent startup timing",
)

// TraceStartupPhase emits a timing log line for the given agent
// startup phase. Time is taken from the log handler at write time;
// the receiving test correlates lines by agent_id and the phase name.
func TraceStartupPhase(agentID, phase string) {
	startupTracer.Log("agent_id", agentID, "phase", phase)
}
