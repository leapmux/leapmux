package providerkit

import (
	"math"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

// A window reads as allowed below 80 percent, as a warning from 80 percent,
// and as exceeded from all of it, overage included.
func TestRateLimitStatusForUtilization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		utilization float64
		want        string
	}{
		{math.Inf(-1), contracts.RateLimitStatusAllowed},
		{-0.5, contracts.RateLimitStatusAllowed},
		{0, contracts.RateLimitStatusAllowed},
		{math.SmallestNonzeroFloat64, contracts.RateLimitStatusAllowed},
		{0.42, contracts.RateLimitStatusAllowed},
		{math.Nextafter(RateLimitWarningUtilization, 0), contracts.RateLimitStatusAllowed},
		{RateLimitWarningUtilization, contracts.RateLimitStatusAllowedWarning},
		// A percentage that a caller divides by 100 meets the same boundary.
		{float64(80) / 100, contracts.RateLimitStatusAllowedWarning},
		{math.Nextafter(RateLimitWarningUtilization, 1), contracts.RateLimitStatusAllowedWarning},
		{0.99, contracts.RateLimitStatusAllowedWarning},
		{math.Nextafter(RateLimitExceededUtilization, 0), contracts.RateLimitStatusAllowedWarning},
		{RateLimitExceededUtilization, contracts.RateLimitStatusExceeded},
		{float64(100) / 100, contracts.RateLimitStatusExceeded},
		{1.5, contracts.RateLimitStatusExceeded},
		{math.MaxFloat64, contracts.RateLimitStatusExceeded},
		{math.Inf(1), contracts.RateLimitStatusExceeded},
		// A value that is not a number states no use.
		{math.NaN(), contracts.RateLimitStatusAllowed},
	} {
		assert.Equal(t, tc.want, RateLimitStatusForUtilization(tc.utilization), "utilization %v", tc.utilization)
	}
}
