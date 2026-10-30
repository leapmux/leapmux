package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
// Each accepted identity keeps its exact bytes. A whitespace-only identity is invalid.
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
	if strings.TrimSpace(w.ReportID) == "" {
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

// CaptureSubagentReport freezes the report bytes and exact decorated transcript writer.
func CaptureSubagentReport(writer TranscriptServices, write SubagentReportWrite) (CapturedTranscript, bool, error) {
	payload, err := write.NotificationPayload()
	if err != nil || payload == nil {
		return CapturedTranscript{}, false, err
	}
	if writer == nil {
		return CapturedTranscript{}, false, errors.New("the subagent report has no writer")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return CapturedTranscript{}, false, fmt.Errorf("marshal subagent report: %w", err)
	}
	sum := sha256.Sum256([]byte(write.ReportID))
	content := MessageContent{Original: encoded, IdempotencyKey: fmt.Sprintf("subagent-report:%x", sum)}
	return CaptureTranscript(writer, content, SpanInfo{}), true, nil
}

// CaptureChildSubagentReport resolves the required child before the write can be delayed.
func CaptureChildSubagentReport(services ProviderServices, write ChildSubagentReportWrite) (CapturedTranscript, bool, error) {
	if services == nil {
		return CapturedTranscript{}, false, errors.New("the child subagent report has no services")
	}
	if write.RowKey == "" {
		return CapturedTranscript{}, false, errors.New("child subagent report has no row key")
	}
	childID, _, found, err := services.LookupBackgroundTask(write.RowKey)
	if err != nil {
		return CapturedTranscript{}, false, err
	}
	if !found || childID == "" {
		return CapturedTranscript{}, false, fmt.Errorf("subagent report child for row %q is unavailable", write.RowKey)
	}
	child := services.ChildSink(childID)
	if child == nil {
		return CapturedTranscript{}, false, fmt.Errorf("subagent report child %q is unavailable", childID)
	}
	return CaptureSubagentReport(child, write.Write)
}
