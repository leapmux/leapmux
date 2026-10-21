package providerkit

import (
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// CompactionEndOutcome states why a compaction did not finish.
type CompactionEndOutcome string

const (
	CompactionEndFailed  CompactionEndOutcome = contracts.CompactionEndOutcomeFailed
	CompactionEndAborted CompactionEndOutcome = contracts.CompactionEndOutcomeAborted
	// CompactionEndCompleted states a compaction that finished with no native
	// end frame to store. The reader treats an end with no outcome and no
	// error as completed, so the row omits the outcome field.
	CompactionEndCompleted CompactionEndOutcome = ""
)

// PersistCompactionEnded records the end that the provider did not send.
// Pass a detail only when no other row states the reason.
func PersistCompactionEnded(sink LeapMuxNotificationServices, outcome CompactionEndOutcome, detail string) {
	if outcome != CompactionEndFailed && outcome != CompactionEndAborted && outcome != CompactionEndCompleted {
		slog.Warn("unknown compaction end outcome", "outcome", outcome)
		outcome = CompactionEndFailed
	}
	content := map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeCompactionEnded,
	}
	if outcome != CompactionEndCompleted {
		content[contracts.NotificationFieldOutcome] = string(outcome)
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		content[contracts.NotificationFieldError] = detail
	}
	sink.PersistLeapMuxNotification(content)
}

// CompactionStateServices publishes live compaction state for one transcript.
type CompactionStateServices interface {
	BroadcastSessionInfo(map[string]interface{})
}

// PublishCompaction reports a running compaction with its optional native trigger.
func PublishCompaction(sink CompactionStateServices, trigger string) {
	value := make(map[string]interface{})
	if trigger != "" {
		value[contracts.CompactionFieldTrigger] = trigger
	}
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyCompaction: value})
}

// EndCompaction withdraws the live compaction with an explicit null.
func EndCompaction(sink CompactionStateServices) {
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyCompaction: nil})
}
