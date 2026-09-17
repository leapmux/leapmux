package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canonicalJSON is what makes the stored supplement's byte compare a true test of
// sameness: two producers of one value must store one set of bytes.
func TestCanonicalJSONSortsEveryObjectItReaches(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, in, want string }{
		{"a flat object", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"a nested object", `{"z":{"d":1,"c":2},"a":3}`, `{"a":3,"z":{"c":2,"d":1}}`},
		{"objects inside an array", `[{"b":1,"a":2},{"d":3,"c":4}]`, `[{"a":2,"b":1},{"c":4,"d":3}]`},
		{"an array keeps its ORDER", `{"a":[3,1,2]}`, `{"a":[3,1,2]}`},
		{"a scalar", `  "text"  `, `"text"`},
		{"whitespace alone", "{\n  \"a\" : 1\n}", `{"a":1}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalJSON(json.RawMessage(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// Two spellings of ONE value must reach the same bytes. This is the property the
// enrichment path depends on, stated directly.
func TestCanonicalJSONGivesTwoSpellingsOneForm(t *testing.T) {
	t.Parallel()
	left, err := canonicalJSON(json.RawMessage(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call"}}`))
	require.NoError(t, err)
	right, err := canonicalJSON(json.RawMessage(`{"payload":{"toolCallId":"call","kind":"scheduled"},"type":"tool.updated"}`))
	require.NoError(t, err)
	assert.Equal(t, string(left), string(right))
}

// A number keeps its ORIGINAL bytes. Decoding into `any` would round every integer
// through a float64, and these payloads carry counters past 2^53.
func TestCanonicalJSONKeepsANumberExact(t *testing.T) {
	t.Parallel()
	got, err := canonicalJSON(json.RawMessage(`{"wide":9007199254740993,"small":1.5e300}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"small":1.5e300,"wide":9007199254740993}`, string(got))
	assert.Contains(t, string(got), `9007199254740993`)
}

// JSONCanonicalEqual is what every "did this supplement change anything?" decision
// asks. The bytes themselves must stay as the agent sent them, so the order
// insensitivity lives here and never in what gets stored.
func TestJSONCanonicalEqualReadsThroughKeyOrder(t *testing.T) {
	t.Parallel()
	assert.True(t, JSONCanonicalEqual(
		json.RawMessage(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call"}}`),
		json.RawMessage(`{"payload":{"toolCallId":"call","kind":"scheduled"},"type":"tool.updated"}`)))
	assert.False(t, JSONCanonicalEqual(
		json.RawMessage(`{"payload":{"toolCallId":"call"}}`),
		json.RawMessage(`{"payload":{"toolCallId":"other"}}`)))
	// An ARRAY carries its order, so two orders are two values.
	assert.False(t, JSONCanonicalEqual(json.RawMessage(`{"a":[1,2]}`), json.RawMessage(`{"a":[2,1]}`)))
	// Identical bytes never reach the parse.
	assert.True(t, JSONCanonicalEqual(nil, nil))
	assert.False(t, JSONCanonicalEqual(nil, json.RawMessage(`{}`)))
	// What does not parse compares only by its bytes, so a damaged supplement is never
	// mistaken for the one already stored.
	assert.False(t, JSONCanonicalEqual(json.RawMessage(`{"a":`), json.RawMessage(`{"a":1}`)))
	assert.True(t, JSONCanonicalEqual(json.RawMessage(`{"a":`), json.RawMessage(`{"a":`)))
}

// `json.Marshal` escapes `<`, `>` and `&`; `json.Compact` does not. The two answered
// differently for ONE value depending on how deep it sat, so a scalar at the top level
// read as changed while the same scalar inside an object read as unchanged.
func TestCanonicalJSONEscapesAtEveryDepthAlike(t *testing.T) {
	t.Parallel()
	nested, err := canonicalJSON(json.RawMessage(`{"a":"<&>"}`))
	require.NoError(t, err)
	scalar, err := canonicalJSON(json.RawMessage(`"<&>"`))
	require.NoError(t, err)
	assert.Equal(t, `{"a":`+string(scalar)+`}`, string(nested))
	assert.True(t, JSONCanonicalEqual(json.RawMessage(`"<"`), json.RawMessage(`"\u003c"`)))
	assert.True(t, JSONCanonicalEqual(json.RawMessage(`{"a":"<"}`), json.RawMessage(`{"a":"\u003c"}`)))
}

// A KNOWN limitation, stated so the next reader does not discover it as a bug: the
// decode is into a map, so a repeated key keeps the last one and two spellings of one
// document compare equal. Every producer here is a Go marshaler, which never emits one.
func TestCanonicalJSONCollapsesARepeatedKey(t *testing.T) {
	t.Parallel()
	got, err := canonicalJSON(json.RawMessage(`{"a":1,"a":2}`))
	require.NoError(t, err)
	assert.Equal(t, `{"a":2}`, string(got))
	assert.True(t, JSONCanonicalEqual(json.RawMessage(`{"a":1,"a":2}`), json.RawMessage(`{"a":2}`)))
}

func TestCanonicalJSONRefusesWhatIsNotJSON(t *testing.T) {
	t.Parallel()
	_, err := canonicalJSON(json.RawMessage(`{"a":`))
	assert.Error(t, err)
	// Nothing at all stays nothing: an absent supplement is not an error.
	got, err := canonicalJSON(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
