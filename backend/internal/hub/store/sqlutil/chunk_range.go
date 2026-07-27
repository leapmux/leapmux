package sqlutil

// ChunkRange walks [0, total) in chunks of at most size, invoking fn with each
// half-open [start, end) window. It stops at the first error fn returns.
//
// It exists because every bulk tab-index path repeats the identical index
// arithmetic and differs only in what it does with the window: the runtime-
// composed `?`-placeholder statements in this package (sqlite, mysql) and the
// column-major array binds in the postgres adapter (which cannot reuse
// BulkUpsertTabs/BulkDeleteTabs, because those compose SQL while postgres binds
// sqlc-generated unnest(...::TEXT[]) queries). The window arithmetic is the one
// thing all of them DO share, so it lives here once.
func ChunkRange(total, size int, fn func(start, end int) error) error {
	for start := 0; start < total; start += size {
		if err := fn(start, min(start+size, total)); err != nil {
			return err
		}
	}
	return nil
}
