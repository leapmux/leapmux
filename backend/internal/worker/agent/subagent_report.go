package agent

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

type subagentReport struct {
	Label  string
	Text   string
	Status string
}

// persistSubagentReport writes one provider-neutral report into a transcript.
// The shared envelope lets every provider use the same extractor and renderer.
func persistSubagentReport(sink SessionServices, report subagentReport) bool {
	report.Text = strings.TrimSpace(report.Text)
	if sink == nil || report.Text == "" {
		return false
	}
	payload := map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeSubagentReport,
		contracts.NotificationFieldText: report.Text,
	}
	if report.Label = strings.TrimSpace(report.Label); report.Label != "" {
		payload[contracts.NotificationFieldLabel] = report.Label
	}
	if report.Status = strings.TrimSpace(report.Status); report.Status != "" {
		payload[contracts.NotificationFieldStatus] = report.Status
	}
	sink.PersistLeapMuxNotification(payload)
	return true
}
