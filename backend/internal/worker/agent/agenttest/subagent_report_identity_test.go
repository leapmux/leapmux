package agenttest

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

func TestSinkSubagentReportRevivalScopingAndNormalization(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	rawKey := strings.Repeat("k", bgtask.RowKeyByteLimit+1)
	normalizedKey := bgtask.NormalizeRowKey(rawKey)
	require.NotEqual(t, rawKey, normalizedKey)

	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		SpawnSpanID:      "spawn-overlong",
		ProviderChildKey: rawKey,
		Title:            "Overlong key child",
	})
	require.NoError(t, err)
	child := sink.Child(childID)

	childWrite := func(text string) agent.ChildSubagentReportWrite {
		return agent.ChildSubagentReportWrite{
			RowKey: rawKey,
			Write: agent.SubagentReportWrite{
				ReportID: "rep",
				Report:   agent.SubagentReport{Text: text},
			},
		}
	}
	rootWrite := func(text string) agent.SubagentReportWrite {
		return agent.SubagentReportWrite{
			ReportID: "rep",
			RowKey:   rawKey,
			Report:   agent.SubagentReport{Text: text},
		}
	}

	// Run 1: both child and root-scoped writes store once and deduplicate on replay.
	stored, err := sink.PersistChildSubagentReport(childWrite("Run 1 child report"))
	require.NoError(t, err)
	assert.True(t, stored)
	stored, err = sink.PersistChildSubagentReport(childWrite("Run 1 child replay"))
	require.NoError(t, err)
	assert.False(t, stored)

	stored, err = sink.PersistSubagentReport(rootWrite("Run 1 root report"))
	require.NoError(t, err)
	assert.True(t, stored)
	stored, err = sink.PersistSubagentReport(rootWrite("Run 1 root replay"))
	require.NoError(t, err)
	assert.False(t, stored)

	// Close and revive increments ReviveCount once; reviving while running is a no-op.
	require.NoError(t, sink.CloseBackgroundTask(rawKey, bgtask.StatusSucceeded))
	require.NoError(t, sink.ReviveBackgroundTask(rawKey))
	row, ok := sink.BackgroundTask(normalizedKey)
	require.True(t, ok)
	assert.Equal(t, int64(1), row.ReviveCount)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	require.NoError(t, sink.ReviveBackgroundTask(rawKey))
	row, ok = sink.BackgroundTask(normalizedKey)
	require.True(t, ok)
	assert.Equal(t, int64(1), row.ReviveCount, "reviving a running row must not increment ReviveCount")

	// Run 2: reusing the same ReportID stores new reports under the revived epoch and deduplicates replays.
	stored, err = sink.PersistChildSubagentReport(childWrite("Run 2 child report"))
	require.NoError(t, err)
	assert.True(t, stored)
	stored, err = sink.PersistChildSubagentReport(childWrite("Run 2 child replay"))
	require.NoError(t, err)
	assert.False(t, stored)

	stored, err = sink.PersistSubagentReport(rootWrite("Run 2 root report"))
	require.NoError(t, err)
	assert.True(t, stored)
	stored, err = sink.PersistSubagentReport(rootWrite("Run 2 root replay"))
	require.NoError(t, err)
	assert.False(t, stored)

	childReports := child.LeapMuxNotifications()
	require.Len(t, childReports, 2)
	assert.Equal(t, "Run 1 child report", childReports[0]["text"])
	assert.Equal(t, "Run 2 child report", childReports[1]["text"])

	rootReports := sink.LeapMuxNotifications()
	require.Len(t, rootReports, 2)
	assert.Equal(t, "Run 1 root report", rootReports[0]["text"])
	assert.Equal(t, "Run 2 root report", rootReports[1]["text"])

	// A non-empty RowKey absent from bgTasks persists unscoped and still deduplicates on replay.
	missingWrite := agent.SubagentReportWrite{
		ReportID: "missing-row-rep",
		RowKey:   "absent-row-key",
		Report:   agent.SubagentReport{Text: "Unscoped report"},
	}
	stored, err = sink.PersistSubagentReport(missingWrite)
	require.NoError(t, err)
	assert.True(t, stored)
	stored, err = sink.PersistSubagentReport(missingWrite)
	require.NoError(t, err)
	assert.False(t, stored)
	require.Len(t, sink.LeapMuxNotifications(), 3)
	assert.Equal(t, "Unscoped report", sink.LeapMuxNotifications()[2]["text"])
}
