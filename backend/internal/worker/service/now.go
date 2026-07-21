package service

import "time"

// nowMillis returns the current instant floored to the millisecond, in UTC.
// Message and notification stamps must be minted this way so the
// live-streamed value is byte-identical to the persisted row: created_at is
// stored at ms precision (the strftime wrap in the queries) and the proto
// echo formats via timefmt (ms truncation). A raw time.Now() would carry
// sub-millisecond residue the storage floors away, making the streamed and
// persisted stamps drift apart.
func nowMillis() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}
