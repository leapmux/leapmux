package providerkit

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialogTimeoutMillis(t *testing.T) {
	for raw, want := range map[string]int64{
		`30000`:   30000,
		` 1 `:     1,
		`1.2`:     2,
		`0.001`:   1,
		`2.5e3`:   2500,
		`1e300`:   math.MaxInt64,
		`9.3e18`:  math.MaxInt64,
		`9.22e18`: 9220000000000000000,
	} {
		got := DialogTimeoutMillis(json.RawMessage(raw))
		require.NotNil(t, got, raw)
		assert.Equal(t, want, *got, raw)
	}
	for _, raw := range []string{``, `null`, `0`, `-0`, `-1`, `-0.5`, `"30000"`, `true`, `{}`, `[30000]`, `1e400`} {
		assert.Nil(t, DialogTimeoutMillis(json.RawMessage(raw)), "%q states no deadline", raw)
	}
}

func TestDialogTimeoutDuration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		timeout float64
		want    time.Duration
	}{
		{name: "zero", timeout: 0, want: 0},
		{name: "negative zero", timeout: math.Copysign(0, -1), want: 0},
		{name: "negative", timeout: -5, want: 0},
		{name: "NaN", timeout: math.NaN(), want: 0},
		{name: "+Inf", timeout: math.Inf(1), want: 0},
		{name: "-Inf", timeout: math.Inf(-1), want: 0},
		{name: "huge", timeout: 1e300, want: 0},
		{name: "at max", timeout: MaxDialogTimeoutMillis, want: 0},
		{name: "just below max", timeout: math.Nextafter(MaxDialogTimeoutMillis, 0), want: time.Duration(9223372036853997568)},
		{name: "max minus one ms", timeout: MaxDialogTimeoutMillis - 1, want: time.Duration(9223372036853000192)},
		{name: "sub-nanosecond truncates to zero", timeout: 1e-9, want: 0},
		{name: "one nanosecond", timeout: 1e-6, want: time.Nanosecond},
		{name: "30 seconds", timeout: 30000, want: 30 * time.Second},
		{name: "fractional millis", timeout: 1.5, want: 1500 * time.Microsecond},
	} {
		assert.Equal(t, tc.want, DialogTimeoutDuration(tc.timeout), tc.name)
	}
}
