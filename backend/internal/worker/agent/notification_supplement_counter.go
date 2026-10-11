package agent

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

// init installs the authoritative stored-supplement parser as the counter
// behind the Worker storage's notification_entry_count generated column.
// The column's SQLite function lives in the storage package; this package
// owns the parser, and the storage package cannot import this one (the
// terminal package's tests open it, and the launch path reaches terminal),
// so the parser arrives through that package's install seam at init.
func init() {
	workerdb.InstallNotificationSupplementCounter(countStoredNotificationEntries)
}

// countStoredNotificationEntries answers one stored supplement's journal
// length through the single parser every reader shares.
func countStoredNotificationEntries(supplement []byte, compression leapmuxv1.ContentCompression) (int64, error) {
	decompressed, err := msgcodec.Decompress(supplement, compression)
	if err != nil {
		return 0, err
	}
	parsed, err := ParseStoredMessageSupplement(decompressed)
	if err != nil {
		return 0, err
	}
	return int64(len(parsed.NotificationJournal())), nil
}
