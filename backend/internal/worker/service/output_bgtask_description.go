package service

import (
	"context"

	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func (sink *agentOutputSink) UpdateBackgroundTaskDescription(rowKey, description string) error {
	description = (bgtask.Upsert{Description: description}).Clean().Description
	return sink.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return sink.h.withBgTaskRow(root, key, sink.outputGeneration, func(ctx context.Context, reg bgTaskView, _ *bgTaskEphemeral, existing bgtask.Item, _ bool, _ func() (int, error)) error {
			if existing.Description == description {
				return nil
			}
			now := nowMillis()
			rows, err := reg.queries.UpdateAgentBackgroundTaskDescription(ctx, db.UpdateAgentBackgroundTaskDescriptionParams{
				OwnerAgentID: root, RowKey: key, Description: description, UpdatedAt: sqltime.NewSQLiteTime(now),
			})
			if err != nil {
				return err
			}
			if rows == 0 {
				return errBackgroundTaskSuperseded
			}
			if index := reg.cache.indexOf(key); index >= 0 {
				reg.cache.Rows[index].Description = description
				reg.cache.Rows[index].UpdatedAt = now
			}
			return nil
		})
	})
}
