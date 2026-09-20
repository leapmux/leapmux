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
	assert.True(t, persistSubagentReport(sink, subagentReport{Label: "  Parser reviewer  ", Text: "\n**Report**\n", Status: " flagged "}))

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
	assert.False(t, persistSubagentReport(sink, subagentReport{Label: "Reviewer", Text: " \n\t "}))
	assert.Empty(t, sink.LeapMuxNotifications())
}
