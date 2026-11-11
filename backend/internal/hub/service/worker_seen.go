package service

import (
	"context"
	"log/slog"
	"time"
)

const (
	workerSeenWriteTimeout  = 5 * time.Second
	workerSeenWriteTimerTag = "worker-seen-write"
)

// startWorkerSeenRecorder serializes one connection's last-seen writes.
// Recording never waits for persistence, so a slow database cannot stop pings.
// One pending notification preserves the latest observation while a write runs.
// stop cancels the writer and joins it before the stream handler returns.
func (s *WorkerConnectorService) startWorkerSeenRecorder(parent context.Context, workerID string) (record func(), stop func()) {
	ctx, cancel := context.WithCancel(parent)
	pending := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-pending:
				if ctx.Err() != nil {
					return
				}
				s.recordWorkerSeen(ctx, workerID)
			}
		}
	}()
	return func() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case pending <- struct{}{}:
			default:
			}
		}, func() {
			cancel()
			<-done
		}
}

func (s *WorkerConnectorService) recordWorkerSeen(ctx context.Context, workerID string) {
	writeCtx, release := s.workerStoreContext(ctx, workerSeenWriteTimeout, workerSeenWriteTimerTag)
	defer release()
	if err := s.store.Workers().UpdateLastSeen(writeCtx, workerID); err != nil && ctx.Err() == nil {
		slog.Warn("failed to update worker last seen", "worker_id", workerID, "error", err)
	}
}
