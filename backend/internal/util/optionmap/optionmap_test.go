package optionmap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMap_Get(t *testing.T) {
	m := Map{"model": "opus"}
	assert.Equal(t, "opus", m.Get("model"))
	assert.Equal(t, "", m.Get("absent"))
	var nilMap Map
	assert.Equal(t, "", nilMap.Get("model"), "Get is nil-safe")
}

func TestMap_Clone(t *testing.T) {
	src := Map{"model": "opus"}
	clone := src.Clone()
	clone["model"] = "sonnet"
	clone["effort"] = "high"
	assert.Equal(t, "opus", src["model"], "mutating the clone must not touch the source")
	assert.NotContains(t, src, "effort")

	var nilMap Map
	got := nilMap.Clone()
	assert.NotNil(t, got, "Clone of nil is a non-nil empty map")
	assert.Empty(t, got)
}

func TestMap_Merge(t *testing.T) {
	base := Map{"model": "opus", "effort": "high"}
	merged := base.Merge(Map{"effort": "max", "sandbox": "read-only"})
	assert.Equal(t, Map{"model": "opus", "effort": "max", "sandbox": "read-only"}, merged,
		"a non-empty incoming value sets/overwrites")
	assert.Equal(t, Map{"model": "opus", "effort": "high"}, base, "Merge does not mutate the receiver")

	// The delta contract distinguishes the two ways a key can be "not set" in incoming:
	//   - an OMITTED key (absent from incoming) preserves the stored value;
	//   - a key PRESENT with an empty value clears it.
	// Assert both in one merge so the contract the Merge doc states can't silently regress.
	delta := base.Merge(Map{"effort": ""}) // model omitted (preserve), effort empty (delete)
	assert.Equal(t, Map{"model": "opus"}, delta,
		"an omitted key is preserved while a present-but-empty value deletes")
}

func TestMap_Marshal(t *testing.T) {
	// Empties are dropped and keys are sorted (stable output for the CAS string compare).
	assert.Equal(t, `{"effort":"high","model":"opus"}`,
		Map{"model": "opus", "effort": "high", "blank": ""}.Marshal())
	assert.Equal(t, "{}", Map{}.Marshal(), "an empty map marshals to {}")
	assert.Equal(t, "{}", Map(nil).Marshal(), "a nil map marshals to {}")
	assert.Equal(t, "{}", Map{"blank": ""}.Marshal(), "an all-empty map marshals to {}")
}

func TestParse(t *testing.T) {
	assert.Equal(t, Map{"model": "opus"}, Parse(`{"model":"opus","blank":""}`),
		"empty values are dropped on parse")
	assert.Equal(t, Map{}, Parse(""), "an empty string parses to a non-nil empty map")
	assert.Equal(t, Map{}, Parse("not json"), "invalid JSON falls back to an empty map")
	assert.Equal(t, Map{}, Parse("null"), "a JSON null parses to a non-nil empty map")
}

// TestRoundTrip_MarshalParse is the CAS-relevant invariant: a parsed map re-marshals to the
// same canonical string, so a re-read row string-compares equal to its re-encoding.
func TestRoundTrip_MarshalParse(t *testing.T) {
	original := Map{"model": "opus", "effort": "high", "sandbox": "read-only"}
	encoded := original.Marshal()
	assert.Equal(t, encoded, Parse(encoded).Marshal(), "marshal->parse->marshal is stable")
}
