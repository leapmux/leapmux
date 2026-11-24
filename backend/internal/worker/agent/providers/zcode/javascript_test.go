package zcode

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeJSONProbe holds each character that encoding/json spells differently
// from JavaScript's JSON.stringify: the HTML characters, which json.Marshal
// escapes, and the line and paragraph separators, which encoding/json escapes
// even with HTML escaping off. The backslash before a separator proves that an
// escaped backslash stays one escape.
const zcodeJSONProbe = "<a&b>\\\u2028\u2029"

// zcodeJSONProbeJSON is zcodeJSONProbe as JSON.stringify writes it.
const zcodeJSONProbeJSON = "\"<a&b>\\\\\u2028\u2029\""

func TestZCodeJSNumber_UsesNativeStringGrammar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  float64
		ok    bool
	}{
		{value: "\uFEFF1\uFEFF", want: 1, ok: true},
		{value: "\u00851\u0085", ok: false},
		{value: "0x10", want: 16, ok: true},
		{value: "0b10", want: 2, ok: true},
		{value: "0o10", want: 8, ok: true},
		{value: "+0x10", ok: false},
		{value: "0x1p0", ok: false},
		{value: "1_0", ok: false},
		{value: "Inf", ok: false},
		{value: "Infinity", want: math.Inf(1), ok: true},
		{value: "1e999", want: math.Inf(1), ok: true},
		{value: ".5", want: 0.5, ok: true},
		{value: "-0", want: math.Copysign(0, -1), ok: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(test.value)
			require.NoError(t, err)
			got, ok := zcodeJSNumber(raw)
			assert.Equal(t, test.ok, ok)
			if ok && test.ok {
				assert.Equal(t, test.want, got)
				if got == 0 {
					assert.Equal(t, math.Signbit(test.want), math.Signbit(got))
				}
			}
		})
	}
	_, ok := zcodeJSONNumberLiteral(json.RawMessage("\u00851\u0085"))
	assert.False(t, ok, "native string whitespace cannot broaden JSON whitespace")
}
