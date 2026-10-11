package grok

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrokFailedWorkflowRetainsItsNativePauseMessage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	update := map[string]any{
		"sessionUpdate": "workflow_updated", "run_id": "native-error-run", "name": "native-script-error",
		"objective": "Compute one native error.", "status": "failed", "pause_message": "native computed error77",
	}
	a.HandleOutput(notification(t, grokTestSession, update))
	row, present := sink.BackgroundTask("workflow:native-error-run")
	require.True(t, present)
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	assert.Equal(t, "native-script-error: Compute one native error.", row.Title)
	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1, "a native failed run must retain its actual failure text")
	assert.Equal(t, row.Title, reports[0]["label"])
	assert.Equal(t, "failed", reports[0]["status"])
	assert.Equal(t, "native computed error77", reports[0]["text"])
	a.HandleOutput(notification(t, grokTestSession, update))
	assert.Len(t, sink.LeapMuxNotifications(), 1, "a repeated final update must retain one report")
}

func TestGrokWorkflowReportUsesTheNativeFieldForItsFinalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		status  string
		message string
		summary string
		want    string
	}{
		{name: "failed native text", status: "failed", message: " \n native error77 文 \n", summary: "old summary", want: " \n native error77 文 \n"},
		{name: "large error", status: "failed", message: strings.Repeat("error77 文\n", 1024), want: strings.Repeat("error77 文\n", 1024)},
		{name: "completed summary", status: "complete", message: "old pause", summary: " \n native output42 文 \n", want: " \n native output42 文 \n"},
		{name: "failed empty error", status: "failed", summary: "old summary"},
		{name: "failed whitespace error", status: "failed", message: " \t\n", summary: "old summary"},
		{name: "completed empty summary", status: "complete", message: "old pause"},
		{name: "active", status: "active", message: "not final", summary: "old summary"},
		{name: "paused", status: "infra_paused", message: "not final", summary: "old summary"},
		{name: "blocked", status: "blocked", message: "not final", summary: "old summary"},
		{name: "budget limited", status: "budget_limited", message: "not final", summary: "old summary"},
		{name: "cancelled", status: "cancelled", message: "not a native failure", summary: "old summary"},
		{name: "interrupted", status: "interrupted", message: "not a native failure", summary: "old summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(notification(t, grokTestSession, map[string]any{
				"sessionUpdate": "workflow_updated", "run_id": "native-run", "name": "native-script",
				"status": tc.status, "pause_message": tc.message, "result_summary": tc.summary,
			}))
			reports := subagentReports(&sink.Sink)
			if tc.want == "" {
				assert.Empty(t, reports)
				return
			}
			require.Len(t, reports, 1)
			assert.Equal(t, tc.want, reports[0]["text"])
			assert.Equal(t, "native-script", reports[0]["label"])
			assert.Equal(t, bgtask.StatusWire(grokWorkflowStatus(tc.status)), reports[0]["status"])
		})
	}
}

func TestGrokFailedWorkflowReportsALateErrorOncePerRun(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	update := func(runID, message string) []byte {
		return notification(t, grokTestSession, map[string]any{
			"sessionUpdate": "workflow_updated", "run_id": runID, "name": "native-script-error",
			"status": "failed", "pause_message": message,
		})
	}
	a.HandleOutput(update("run-1", ""))
	assert.Empty(t, subagentReports(&sink.Sink))
	a.HandleOutput(update("run-1", "native error77"))
	a.HandleOutput(update("run-1", "native error77"))
	a.HandleOutput(update("run-2", "native error77"))

	reports := subagentReports(&sink.Sink)
	require.Len(t, reports, 2, "separate native runs retain separate reports")
	for _, report := range reports {
		assert.Equal(t, "native error77", report["text"])
		assert.Equal(t, "failed", report["status"])
	}
	for _, runID := range []string{"run-1", "run-2"} {
		row, present := sink.BackgroundTask("workflow:" + runID)
		require.True(t, present)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
	}
}
