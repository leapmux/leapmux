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

// SubagentReportTarget selects the transcript that owns a report.
type SubagentReportTarget uint8

const (
	SubagentReportCurrentTranscript SubagentReportTarget = iota
	SubagentReportChildTranscript
)

// SubagentReportWrite identifies one report independently from its content.
// ReportID is stable across protocol replays. RowKey is required for a child target.
type SubagentReportWrite struct {
	ReportID string
	RowKey   string
	Target   SubagentReportTarget
	Report   SubagentReport
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
	switch w.Target {
	case SubagentReportCurrentTranscript:
	case SubagentReportChildTranscript:
		if strings.TrimSpace(w.RowKey) == "" {
			return nil, fmt.Errorf("child subagent report has no row key")
		}
	default:
		return nil, fmt.Errorf("subagent report has unknown target %d", w.Target)
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
	payload, err := write.NotificationPayload()
	if err != nil {
		slog.Warn("invalid subagent report", "report_id", write.ReportID, "row_key", write.RowKey, "error", err)
		return false
	}
	if payload == nil {
		return false
	}
	stored, err := sink.PersistSubagentReport(write)
	if err != nil {
		slog.Warn("persist subagent report", "report_id", write.ReportID, "row_key", write.RowKey, "error", err)
		return false
	}
	return stored
}
