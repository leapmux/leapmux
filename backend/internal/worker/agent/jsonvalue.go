package agent

import (
	"bytes"
	"encoding/json"
)

// jsonEqual compares two encoded values by their compact form, so a difference in
// spacing alone does not read as a different value.
//
// Two providers ask the same question of their own frames: Pi asks whether a second
// resolve pass would produce the value it already has, and Codex asks whether an
// account snapshot moved since the row it wrote. Neither reads a provider's shape, so
// the helper belongs to no provider.
func jsonEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var leftCompact, rightCompact bytes.Buffer
	if json.Compact(&leftCompact, left) != nil || json.Compact(&rightCompact, right) != nil {
		return false
	}
	return bytes.Equal(leftCompact.Bytes(), rightCompact.Bytes())
}
