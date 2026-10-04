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
