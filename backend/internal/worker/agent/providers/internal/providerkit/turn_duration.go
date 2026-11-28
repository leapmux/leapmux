package providerkit

import "time"

// TurnDurationMillis measures one turn in milliseconds.
// It returns nil when startedAt is zero or endedAt precedes startedAt so
// callers omit absent or backwards durations instead of emitting a false 0ms.
func TurnDurationMillis(startedAt, endedAt time.Time) *int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return nil
	}
	ms := endedAt.Sub(startedAt).Milliseconds()
	return &ms
}
