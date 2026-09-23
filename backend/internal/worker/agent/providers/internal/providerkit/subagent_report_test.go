package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistSubagentReportNormalizesOneSharedEnvelope(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	assert.True(t, PersistSubagentReport(sink, agent.SubagentReportWrite{
		ReportID: "report-1",
		Report:   agent.SubagentReport{Label: "  Parser reviewer  ", Text: "\n**Report**\n", Status: " flagged "},
	}))

	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, contracts.NotificationTypeSubagentReport, reports[0][contracts.NotificationFieldType])
	assert.Equal(t, "Parser reviewer", reports[0][contracts.NotificationFieldLabel])
	assert.Equal(t, "**Report**", reports[0][contracts.NotificationFieldText])
	assert.Equal(t, "flagged", reports[0][contracts.NotificationFieldStatus])
}

func TestPersistSubagentReportDropsBlankText(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	assert.False(t, PersistSubagentReport(sink, agent.SubagentReportWrite{
		ReportID: "report-1",
		Report:   agent.SubagentReport{Label: "Reviewer", Text: " \n\t "},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistSubagentReportRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	assert.False(t, PersistSubagentReport(sink, agent.SubagentReportWrite{
		ReportID: " \n\t ",
		Report:   agent.SubagentReport{Text: "Report"},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistChildSubagentReportRejectsABlankRowKey(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	assert.False(t, PersistChildSubagentReport(sink, agent.ChildSubagentReportWrite{
		RowKey: " \n\t ",
		Write: agent.SubagentReportWrite{
			ReportID: "report-1",
			Report:   agent.SubagentReport{Text: "Report"},
		},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistSubagentReportDeduplicatesOneIdentity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	write := agent.SubagentReportWrite{ReportID: "report-1", Report: agent.SubagentReport{Text: "Report"}}
	assert.True(t, PersistSubagentReport(sink, write))
	assert.False(t, PersistSubagentReport(sink, write))
	assert.Len(t, sink.LeapMuxNotifications(), 1)
}

func TestPersistSubagentReportResolvesAChildRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	childID, err := sink.EnsureChildAgent("spawn-1", "row-1", "Reviewer")
	require.NoError(t, err)
	write := agent.ChildSubagentReportWrite{
		RowKey: "row-1",
		Write: agent.SubagentReportWrite{
			ReportID: "report-1",
			Report:   agent.SubagentReport{Text: "Report"},
		},
	}
	assert.True(t, PersistChildSubagentReport(sink, write))
	child := sink.Child(childID)
	assert.Len(t, child.LeapMuxNotifications(), 1)
}
