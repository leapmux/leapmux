package providerkit

import (
	"encoding/json"
	"math"
	"time"
)

// MaxDialogTimeoutMillis is the largest dialog timeout in milliseconds that a
// time.Duration can represent without overflow.
const MaxDialogTimeoutMillis = float64(math.MaxInt64 / int64(time.Millisecond))

// DialogTimeoutDuration converts a dialog timeout in milliseconds to a
// time.Duration, returning 0 for non-positive, NaN, or overflowing values.
func DialogTimeoutDuration(timeoutMillis float64) time.Duration {
	if !(timeoutMillis > 0 && timeoutMillis < MaxDialogTimeoutMillis) {
		return 0
	}
	return time.Duration(timeoutMillis * float64(time.Millisecond))
}

// DialogTimeoutMillis reads the wait after which a runtime answers a dialog
// itself, as the dialog's `timeout` field states it: a JSON number of
// milliseconds. It returns nil for zero, a negative number, and a value
// that is no number, which state no deadline. A fraction rounds up, so a
// positive wait never reads as none, and a wait past the limit of int64
// saturates.
func DialogTimeoutMillis(raw json.RawMessage) *int64 {
	value, ok := RawFloat64(raw)
	if !ok || value <= 0 {
		return nil
	}
	var millis int64 = math.MaxInt64
	if ceiling := math.Ceil(value); ceiling < math.MaxInt64 {
		millis = int64(ceiling)
	}
	return &millis
}
