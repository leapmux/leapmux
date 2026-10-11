package service

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentReportKeepsExactNativeIdentity(t *testing.T) {
	t.Parallel()
	for _, nativeID := range []string{" report-1", "report-1 ", "\treport-1"} {
		t.Run(nativeID, func(t *testing.T) {
			t.Parallel()
			svc, sink := setupRootSink(t, "exact-report-identity")
			for _, reportID := range []string{"report-1", nativeID} {
				stored, err := sink.PersistSubagentReport(agent.SubagentReportWrite{ReportID: reportID, Report: agent.SubagentReport{Text: "Same native report."}})
				require.NoError(t, err)
				assert.True(t, stored, "each exact native identity must write its own report")
			}
			for _, reportID := range []string{"report-1", nativeID} {
				stored, err := sink.PersistSubagentReport(agent.SubagentReportWrite{ReportID: reportID, Report: agent.SubagentReport{Text: "Changed replay."}})
				require.NoError(t, err)
				assert.False(t, stored, "an exact replay must keep the first report")
			}
			for _, blank := range []string{"", " \t\n "} {
				stored, err := sink.PersistSubagentReport(agent.SubagentReportWrite{ReportID: blank, Report: agent.SubagentReport{Text: "Refuse the blank identity."}})
				assert.ErrorContains(t, err, "no identity")
				assert.False(t, stored)
			}
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "exact-report-identity"})
			require.NoError(t, err)
			assert.Len(t, rows, 2)
			if len(rows) == 2 {
				assert.NotEqual(t, rows[0].IdempotencyKey, rows[1].IdempotencyKey)
			}
			for _, wrapper := range transcriptMessages(t, svc, "exact-report-identity") {
				messages, ok := wrapper["messages"].([]any)
				require.True(t, ok)
				require.Len(t, messages, 1)
				report, ok := messages[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "Same native report.", report["text"])
			}
		})
	}
}
