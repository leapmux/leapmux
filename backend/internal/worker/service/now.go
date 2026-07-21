package service

import (
	"time"

	"github.com/leapmux/leapmux/internal/util/sqltime"
)

// nowMillis returns the current instant floored to the millisecond, in UTC.
// Message and notification stamps must be minted this way so the
// live-streamed value is byte-identical to the persisted row: created_at is
// stored at ms precision (the SQLiteTime bind floors it) and the proto
// echo formats via timefmt (ms truncation). A raw time.Now() would carry
// sub-millisecond residue the storage floors away, making the streamed and
// persisted stamps drift apart. Delegates to sqltime.FloorMillis so this
// floor can never drift from the one the storage valuers apply.
func nowMillis() time.Time {
	return sqltime.FloorMillis(time.Now())
}
