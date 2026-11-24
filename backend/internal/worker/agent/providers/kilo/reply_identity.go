package kilo

import (
	"strconv"

	"github.com/leapmux/leapmux/internal/util/id"
)

// One native assistant message can produce several assembled transcript rows.
// The prefix identifies the native message and the suffix distinguishes its rows.
func kiloAssistantTextKey(messageID string) string {
	return "kilo-reply:" + strconv.Quote(messageID) + ":" + id.Generate()
}
