package agent

import (
	"errors"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// SubagentReport is the provider-neutral report content that the transcript renders.
type SubagentReport struct {
	Label  string
	Text   string
	Status string
}

// SubagentReportWrite identifies one report independently from its content.
// ReportID is stable across protocol replays.
type SubagentReportWrite struct {
	ReportID string
	Report   SubagentReport
}

// ChildSubagentReportWrite routes one report through a required task row.
type ChildSubagentReportWrite struct {
	RowKey string
	Write  SubagentReportWrite
}

// NotificationPayload returns the normalized LeapMux notification envelope.
func (w SubagentReportWrite) NotificationPayload() (map[string]any, error) {
	w.ReportID = strings.TrimSpace(w.ReportID)
	if w.ReportID == "" {
		return nil, errors.New("subagent report has no identity")
	}
	if strings.TrimSpace(w.Report.Text) == "" {
		return nil, nil
	}
	payload := map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeSubagentReport,
		contracts.NotificationFieldText: w.Report.Text,
	}
	if label := strings.TrimSpace(w.Report.Label); label != "" {
		payload[contracts.NotificationFieldLabel] = label
	}
	if status := strings.TrimSpace(w.Report.Status); status != "" {
		payload[contracts.NotificationFieldStatus] = status
	}
	return payload, nil
}
