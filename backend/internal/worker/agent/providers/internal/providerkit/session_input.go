package providerkit

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// CheckInputSession runs in the same critical section that captures the native input target.
// A nil expected value selects the current session. A supplied empty value never authorizes delivery.
func CheckInputSession(expected *string, current string) error {
	if expected != nil && (*expected == "" || *expected != current) {
		return agent.ErrInputSessionChanged
	}
	return nil
}
