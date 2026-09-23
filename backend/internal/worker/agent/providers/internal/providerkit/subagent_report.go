package providerkit

import (
	"crypto/sha256"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// SubagentReportContentID derives a report id from the report text, for an
// event that carries no id of its own. The same text under the same namespace
// and key gives the same id, so a replayed event deduplicates.
func SubagentReportContentID(namespace, key, text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%s:%s:%x", namespace, key, sum)
}

// PersistSubagentReport applies the shared validation and logs a failed durable write.
func PersistSubagentReport(sink agent.SessionServices, write agent.SubagentReportWrite) bool {
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

// PersistChildSubagentReport validates a child route and logs a failed durable write.
func PersistChildSubagentReport(sink agent.SessionServices, write agent.ChildSubagentReportWrite) bool {
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
