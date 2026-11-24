package providerkit

import (
	"encoding/json"
	"math"
	"testing"

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
