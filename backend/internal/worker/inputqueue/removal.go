package inputqueue

import (
	"context"
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqltime"
)

// RemovedInputRetention is how long the Worker keeps the receipt of an input
// that someone removed from the queue (see Store.Delete). Until PurgeRemoved
// deletes the receipt from agent_input_queue_removed, a retry of the request
// that queued the input finds it and queues nothing. Such a retry is
// EnqueueAgentInput with the same input id, as each run of `leapmux control
// agent send --request-id` sends. After the purge, the retry queues the input
// as a new one.
//
// The window covers a script that retries within minutes, and a job that runs
// again hours or days later, such as a nightly job after a failed night. It
// equals the time for which the Worker keeps a closed agent or terminal
// (cleanupRetention in package service), so what someone removes or closes
// stays for one window. Store.Delete deletes the queue row and its attachments
// immediately, keeping only a small receipt in agent_input_queue_removed until
// the Worker's hourly cleanup purges it within about an hour after the window
// ends.
const RemovedInputRetention = 7 * 24 * time.Hour

// removedInputPurgeBatch caps the rows that one PurgeRemoved call deletes, so
// that one call holds the write lock briefly. The cleanup loop calls it again
// until a call deletes nothing.
const removedInputPurgeBatch = 1000

// purgeRemovedStatement deletes up to a limit of the tombstones whose removal
// lies before a cutoff. The cutoff and removed_at share the canonical layout of
// sqltime, so the text comparison orders them as instants. SQLite finds the
// rows through idx_agent_input_queue_removed; TestQueueStatementsUseIndexes
// pins that plan.
const purgeRemovedStatement = `
	DELETE FROM agent_input_queue_removed
	WHERE rowid IN (SELECT rowid FROM agent_input_queue_removed WHERE removed_at < ? LIMIT ?)`

// PurgeRemoved deletes up to removedInputPurgeBatch tombstone rows of inputs
// that someone removed more than RemovedInputRetention before the instant that
// the store's clock reads, for every agent (text and attachments were already
// cleared by Store.Delete). A row removed exactly RemovedInputRetention
// earlier stays.
//
// The store's clock also stamps each removal, so the cutoff and removed_at
// come from one clock.
func (s *Store) PurgeRemoved(ctx context.Context) (sql.Result, error) {
	cutoff := sqltime.SQLiteNullTimeOf(s.now().Add(-RemovedInputRetention))
	return s.db.ExecContext(ctx, purgeRemovedStatement, cutoff, removedInputPurgeBatch)
}

// PurgeRemoved deletes one batch of the rows that Store.PurgeRemoved deletes.
// The Worker's cleanup loop calls it.
//
// It takes neither the coordinator lock nor queue admission:
//   - A removed row is in no snapshot and in no dispatch, so the purge changes
//     nothing that a queue shows, and it publishes nothing.
//   - The repeat check of enqueue reads a removed row, and SQLite orders the
//     two writes. A retry before the purge queues nothing, and a retry after
//     it queues the input as a new one, which RemovedInputRetention accepts.
//   - The Worker's shutdown stops the cleanup loop before the caller closes
//     the database, so the purge needs no admission to finish first. A pass
//     that runs while the shutdown closes admission then still succeeds.
func (m *Manager) PurgeRemoved(ctx context.Context) (sql.Result, error) {
	return m.store.PurgeRemoved(ctx)
}
