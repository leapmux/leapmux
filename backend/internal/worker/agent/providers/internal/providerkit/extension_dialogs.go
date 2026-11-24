package providerkit

import (
	"encoding/json"
	"math"
)

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
