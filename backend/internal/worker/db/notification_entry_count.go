package db

import (
	"database/sql/driver"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"modernc.org/sqlite"
)

// init registers the deterministic SQLite function behind the messages
// notification_entry_count generated column before any Worker connection
// opens: importing this package is what makes the schema's DDL usable.
// Registration is global for the "sqlite" driver, so connections the generic
// opener serves also see it; that opener itself stays independent of Worker
// message formats. External SQLite writers must register the same function
// or their writes against messages fail.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"leapmux_notification_entry_count", 2, notificationEntryCount)
}

// notificationSupplementCounter counts one stored supplement's notification
// journal entries. The authoritative parser lives in the agent package, which
// installs it at its own init through InstallNotificationSupplementCounter:
// the storage package cannot import the agent package (agent's launch path
// reaches the terminal package whose tests open this one), so the counter
// arrives through this seam instead. Unset, it answers 0 -- every context
// that links no agent stores no journal, so a plain row's count is exactly
// that, and the real binary always links the agent package.
var notificationSupplementCounter func(supplement []byte, compression leapmuxv1.ContentCompression) (int64, error)

// InstallNotificationSupplementCounter installs the authoritative stored-
// supplement parser the generated column counts through. Call it from a
// package init; the counter is process-global like the function itself.
func InstallNotificationSupplementCounter(counter func(supplement []byte, compression leapmuxv1.ContentCompression) (int64, error)) {
	notificationSupplementCounter = counter
}

// notificationEntryCount computes the stored-supplement journal length, so
// the stored count can never disagree with what a reader decodes. It answers
// -1 rather than an SQL error when the stored bytes fail decompression or
// hold invalid private storage: the -1 row stays discoverable by the
// count <> 0 lookup, which then surfaces the corruption when it decodes the
// supplement for real, instead of silently treating the row as one without a
// journal.
func notificationEntryCount(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	supplement, ok := args[0].([]byte)
	if !ok {
		return nil, fmt.Errorf("leapmux_notification_entry_count: the supplemental content must be a BLOB")
	}
	ordinal, ok := args[1].(int64)
	if !ok {
		return nil, fmt.Errorf("leapmux_notification_entry_count: the supplemental compression must be an INTEGER")
	}
	counter := notificationSupplementCounter
	if counter == nil {
		return int64(0), nil
	}
	count, err := counter(supplement, leapmuxv1.ContentCompression(ordinal))
	if err != nil {
		return int64(-1), nil
	}
	return count, nil
}
