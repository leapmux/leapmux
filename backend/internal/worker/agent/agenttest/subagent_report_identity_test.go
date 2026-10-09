package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingSubagentReportKeepsExactNativeIdentity(t *testing.T) {
	t.Parallel()
	for _, nativeID := range []string{" report-1", "report-1 ", "\treport-1"} {
		t.Run(nativeID, func(t *testing.T) {
			t.Parallel()
			sink := &Sink{}
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
			reports := sink.LeapMuxNotifications()
			assert.Len(t, reports, 2)
			for _, report := range reports {
				assert.Equal(t, "Same native report.", report["text"])
			}
		})
	}
}
