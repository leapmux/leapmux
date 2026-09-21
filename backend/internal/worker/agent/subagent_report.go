package agent

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

func subagentReportContentID(namespace, key, text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%s:%s:%x", namespace, key, sum)
}

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
func (w SubagentReportWrite) NotificationPayload() (map[string]interface{}, error) {
	w.ReportID = strings.TrimSpace(w.ReportID)
	w.Report.Text = strings.TrimSpace(w.Report.Text)
	if w.ReportID == "" {
		return nil, fmt.Errorf("subagent report has no identity")
	}
	if w.Report.Text == "" {
		return nil, nil
	}
	payload := map[string]interface{}{
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

// persistSubagentReport applies the shared validation and logs a failed durable write.
func persistSubagentReport(sink SessionServices, write SubagentReportWrite) bool {
	if sink == nil {
		return false
	}
	stored, err := sink.PersistSubagentReport(write)
	if err != nil {
		slog.Warn("persist subagent report", "report_id", write.ReportID, "error", err)
		return false
	}
	return stored
}

// persistChildSubagentReport validates a child route and logs a failed durable write.
func persistChildSubagentReport(sink SessionServices, write ChildSubagentReportWrite) bool {
	if sink == nil {
		return false
	}
	stored, err := sink.PersistChildSubagentReport(write)
	if err != nil {
		slog.Warn("persist child subagent report", "report_id", write.Write.ReportID, "row_key", write.RowKey, "error", err)
		return false
	}
	return stored
}
