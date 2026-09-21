package agent

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistSubagentReportNormalizesOneSharedEnvelope(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	assert.True(t, persistSubagentReport(sink, SubagentReportWrite{
		ReportID: "report-1",
		Report:   SubagentReport{Label: "  Parser reviewer  ", Text: "\n**Report**\n", Status: " flagged "},
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

	sink := &testSink{}
	assert.False(t, persistSubagentReport(sink, SubagentReportWrite{
		ReportID: "report-1",
		Report:   SubagentReport{Label: "Reviewer", Text: " \n\t "},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistSubagentReportRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	assert.False(t, persistSubagentReport(sink, SubagentReportWrite{
		ReportID: " \n\t ",
		Report:   SubagentReport{Text: "Report"},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistChildSubagentReportRejectsABlankRowKey(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	assert.False(t, persistChildSubagentReport(sink, ChildSubagentReportWrite{
		RowKey: " \n\t ",
		Write: SubagentReportWrite{
			ReportID: "report-1",
			Report:   SubagentReport{Text: "Report"},
		},
	}))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestPersistSubagentReportDeduplicatesOneIdentity(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	write := SubagentReportWrite{ReportID: "report-1", Report: SubagentReport{Text: "Report"}}
	assert.True(t, persistSubagentReport(sink, write))
	assert.False(t, persistSubagentReport(sink, write))
	assert.Len(t, sink.LeapMuxNotifications(), 1)
}

func TestPersistSubagentReportResolvesAChildRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	childID, err := sink.EnsureChildAgent("spawn-1", "row-1", "Reviewer")
	require.NoError(t, err)
	write := ChildSubagentReportWrite{
		RowKey: "row-1",
		Write: SubagentReportWrite{
			ReportID: "report-1",
			Report:   SubagentReport{Text: "Report"},
		},
	}
	assert.True(t, persistChildSubagentReport(sink, write))
	child := sink.ChildSink(childID).(*testSink)
	assert.Len(t, child.LeapMuxNotifications(), 1)
}
