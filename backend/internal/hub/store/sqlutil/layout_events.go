package sqlutil

import (
	"context"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// LayoutEventEntriesTable is the table storing encrypted layout event records.
const LayoutEventEntriesTable = "layout_event_entries"

// BulkInsertLayoutEvents inserts rows into layout_event_entries in chunked
// multi-row INSERT statements, sealing each row's fact with its per-row AAD.
// Each row uses 4 placeholders (user_id, sequence, observed_unix_millis, fact).
func BulkInsertLayoutEvents(
	ctx context.Context,
	exec DBExec,
	ks *keystore.Keystore,
	userID string,
	rows []store.LayoutEventRow,
	chunkRows int,
	mapErr func(error) error,
) error {
	if len(rows) == 0 {
		return nil
	}
	return ChunkRange(len(rows), chunkRows, func(start, end int) error {
		chunk := rows[start:end]
		var sb strings.Builder
		sb.Grow(96 + len(chunk)*16)
		sb.WriteString("INSERT INTO ")
		sb.WriteString(LayoutEventEntriesTable)
		sb.WriteString(" (user_id, sequence, observed_unix_millis, fact) VALUES ")
		args := make([]any, 0, len(chunk)*4)
		for i, r := range chunk {
			if i > 0 {
				sb.WriteString(", ")
			}
			sealed, err := ks.SealBytes(r.Fact, LayoutEventEntryFactAAD(userID, r.Sequence))
			if err != nil {
				return err
			}
			sb.WriteString("(?, ?, ?, ?)")
			args = append(args, userID, r.Sequence, r.ObservedUnixMillis, sealed)
		}
		if _, err := exec.ExecContext(ctx, sb.String(), args...); err != nil {
			return mapErr(err)
		}
		return nil
	})
}
