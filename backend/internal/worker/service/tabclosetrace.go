package service

import "github.com/leapmux/leapmux/internal/util/phasetrace"

// tabCloseTracer emits structured timing log lines tagged with
// marker=tab_close_timing when LEAPMUX_TRACE_TAB_CLOSE=1, so that tests
// (e.g. the tab-close-timing e2e) can parse the phase timeline from
// stderr. Off by default — production logs are unaffected.
var tabCloseTracer = phasetrace.New(
	"LEAPMUX_TRACE_TAB_CLOSE",
	"tab_close_timing",
	"tab close timing",
)

// traceTabClosePhase emits a timing log line for the given inspect /
// close tab phase. Time is taken from the log handler at write time;
// the receiving test correlates lines by tab_id and the phase name.
func traceTabClosePhase(op, tabID, phase string) {
	tabCloseTracer.Log("op", op, "tab_id", tabID, "phase", phase)
}
