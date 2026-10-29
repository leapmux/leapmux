package providerkit

import (
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// SlashCommand splits a message that opens with a slash command into the
// command's name and its arguments: "/compact keep the plan" gives "compact"
// and "keep the plan". ok is false for a message that does not open with a
// slash and a name. Leading and trailing white space is ignored.
func SlashCommand(content string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(content)
	command, found := strings.CutPrefix(trimmed, "/")
	if !found {
		return "", "", false
	}
	end := strings.IndexFunc(command, unicode.IsSpace)
	if end < 0 {
		end = len(command)
	}
	if end == 0 {
		return "", "", false
	}
	return command[:end], strings.TrimSpace(command[end:]), true
}

// CheckInputSession runs in the same critical section that captures the native input target.
// A nil expected value selects the current session. A supplied empty value never authorizes delivery.
func CheckInputSession(expected *string, current string) error {
	if expected != nil && (*expected == "" || *expected != current) {
		return agent.ErrInputSessionChanged
	}
	return nil
}
