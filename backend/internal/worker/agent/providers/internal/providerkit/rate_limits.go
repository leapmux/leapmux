package providerkit

import (
	"math"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The used shares of a quota window at which its status changes. A provider
// whose service states a status of its own keeps that status instead.
const (
	// RateLimitWarningUtilization is the share at which a window reads as a
	// warning: 80 percent.
	RateLimitWarningUtilization = 0.8
	// RateLimitExceededUtilization is the share at which a window reads as
	// exceeded: all of it. A share above it is overage, which stays exceeded.
	RateLimitExceededUtilization = 1.0
)

// RateLimitStatusForUtilization is the status of a quota window whose used
// share is utilization: 1 is the whole window. A value that is not a number
// reads as allowed, because it states no use.
func RateLimitStatusForUtilization(utilization float64) string {
	switch {
	case math.IsNaN(utilization):
		return contracts.RateLimitStatusAllowed
	case utilization >= RateLimitExceededUtilization:
		return contracts.RateLimitStatusExceeded
	case utilization >= RateLimitWarningUtilization:
		return contracts.RateLimitStatusAllowedWarning
	default:
		return contracts.RateLimitStatusAllowed
	}
}
