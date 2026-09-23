package providerkit

import (
	"encoding/json"
	"log/slog"
)

// WarnUnmarshal reports invalid provider JSON and returns whether decoding succeeded.
func WarnUnmarshal(data []byte, v any, label string) bool {
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn(label+" unmarshal failed", "error", err)
		return false
	}
	return true
}
