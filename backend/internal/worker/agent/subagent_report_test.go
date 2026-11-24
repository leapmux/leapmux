package agent

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentReportNotificationPayloadPreservesNativeText(t *testing.T) {
	t.Parallel()
	for _, text := range []string{" \n native output42 文 \n", "\uFEFFnative output", strings.Repeat("native output 文\n", 10000)} {
		t.Run(text[:min(len(text), 24)], func(t *testing.T) {
			t.Parallel()
			write := SubagentReportWrite{ReportID: " report-1 ", Report: SubagentReport{Label: " Reviewer ", Text: text, Status: " failed "}}
			original := write
			payload, err := write.NotificationPayload()
			require.NoError(t, err)
			require.NotNil(t, payload)
			assert.Equal(t, text, payload[contracts.NotificationFieldText])
			assert.Equal(t, "Reviewer", payload[contracts.NotificationFieldLabel])
			assert.Equal(t, "failed", payload[contracts.NotificationFieldStatus])
			assert.Equal(t, contracts.NotificationTypeSubagentReport, payload[contracts.NotificationFieldType])
			assert.Equal(t, original, write)
		})
	}
}

func TestSubagentReportNotificationPayloadRejectsBlankIdentity(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{"", " \n\t "} {
		t.Run(identity, func(t *testing.T) {
			t.Parallel()
			payload, err := (SubagentReportWrite{ReportID: identity, Report: SubagentReport{Text: "native output"}}).NotificationPayload()
			require.ErrorContains(t, err, "no identity")
			assert.Nil(t, payload)
		})
	}
}

func TestSubagentReportNotificationPayloadOmitsBlankReportsAndOptionalFields(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", " \n\t "} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			payload, err := (SubagentReportWrite{ReportID: "report-1", Report: SubagentReport{Text: text}}).NotificationPayload()
			require.NoError(t, err)
			assert.Nil(t, payload)
		})
	}
	payload, err := (SubagentReportWrite{ReportID: "report-1", Report: SubagentReport{Text: "native output", Label: " \t ", Status: " \n "}}).NotificationPayload()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeSubagentReport,
		contracts.NotificationFieldText: "native output",
	}, payload)
}

func TestScopeSubagentReportID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		reportID    string
		reviveCount int64
		want        string
	}{
		{name: "empty report ID with negative reviveCount", reportID: "", reviveCount: -1, want: ""},
		{name: "empty report ID with zero reviveCount", reportID: "", reviveCount: 0, want: ""},
		{name: "empty report ID with reviveCount 1", reportID: "", reviveCount: 1, want: ""},
		{name: "empty report ID with reviveCount 2", reportID: "", reviveCount: 2, want: ""},
		{name: "zero reviveCount leaves report ID unchanged", reportID: "report-1", reviveCount: 0, want: "report-1"},
		{name: "negative reviveCount leaves report ID unchanged", reportID: "report-1", reviveCount: -1, want: "report-1"},
		{name: "first revival appends #2", reportID: "report-1", reviveCount: 1, want: "report-1#2"},
		{name: "second revival appends #3", reportID: "report-1", reviveCount: 2, want: "report-1#3"},
		{name: "preserves exact whitespace bytes on revival", reportID: " report-1 ", reviveCount: 1, want: " report-1 #2"},
		{name: "preserves exact whitespace bytes without revival", reportID: " report-1 ", reviveCount: 0, want: " report-1 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ScopeSubagentReportID(tc.reportID, tc.reviveCount))
		})
	}
}
