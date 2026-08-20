package gitutil

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// branchNameRefusalMarkers maps a fixture refusal token to a substring of THIS
// package's message for that rule.
//
// A token rather than a message, because the two copies of this rule word their
// answers differently: Go names the offending character ("must not contain
// '~'") where the browser does not ("Branch name contains invalid characters").
// Pinning the message would either force one side to reword or force the
// fixture to carry both; a token names the RULE, which is the thing that has to
// agree. The browser suite carries its own map.
var branchNameRefusalMarkers = map[string]string{
	"empty":               "must not be empty",
	"too_long":            "must be at most",
	"control_character":   "must not contain control characters",
	"forbidden_character": "must not contain '",
	"at_alone":            "must not be the single character",
	"leading_character":   "must not start with",
	"trailing_character":  "must not end with",
	"lock_component":      "path component that ends with .lock",
	"reflog_syntax":       "must not contain '@{'",
	"double_dot":          "must not contain '..'",
	"double_slash":        "must not contain '//'",
	"slash_dot":           "must not contain '/.'",
}

type branchNameConformanceFixture struct {
	Cases []struct {
		Input struct {
			Text string `json:"text"`
		} `json:"input"`
		Valid   bool   `json:"valid"`
		Refusal string `json:"refusal"`
		Why     string `json:"why"`
	} `json:"cases"`
}

// TestValidateBranchNameConformance reads the corpus that
// frontend/src/lib/validate.test.ts reads.
//
// The rule is twelve branches deep and it is written out TWICE, once per
// language. Before this corpus the only thing holding the copies together was
// that a human edited both in one commit -- and the failure it guards against
// is invisible until a user types the name: the panel offers a branch the
// worker then refuses, or the panel refuses one the repository already holds
// and `for-each-ref` lists, so the branch cannot be acted on from inside
// LeapMux at all.
//
// The same diff built this mechanism for the session ID and for title cleaning
// and did not apply it here.
func TestValidateBranchNameConformance(t *testing.T) {
	t.Parallel()

	const path = "../../../../testdata/branch_name_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/validate.test.ts")

	var fixture branchNameConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that loaded nothing would pass every assertion below while
	// asserting nothing -- the one failure mode a shared corpus must not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)

	for _, c := range fixture.Cases {
		t.Run(c.Why, func(t *testing.T) {
			err := ValidateBranchName(c.Input.Text)
			if c.Valid {
				require.Emptyf(t, c.Refusal, "case %q is valid, so its refusal must be empty", c.Why)
				assert.NoErrorf(t, err, "case %q must be accepted", c.Why)
				return
			}
			require.Errorf(t, err, "case %q must be refused", c.Why)
			marker, ok := branchNameRefusalMarkers[c.Refusal]
			require.Truef(t, ok, "case %q carries an unknown refusal token %q", c.Why, c.Refusal)
			assert.Containsf(t, err.Error(), marker,
				"case %q must report the %s rule", c.Why, c.Refusal)
		})
	}
}

// TestBranchNameCorpusCoversEveryRule keeps the corpus from going stale as the
// rule grows. Every token the marker table names must appear in the fixture, so
// a rule added to one language and not the other cannot hide behind a corpus
// that never exercised it.
func TestBranchNameCorpusCoversEveryRule(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../../testdata/branch_name_conformance.json")
	require.NoError(t, err)
	var fixture branchNameConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))

	seen := map[string]bool{}
	accepted := 0
	for _, c := range fixture.Cases {
		if c.Valid {
			accepted++
			continue
		}
		seen[c.Refusal] = true
	}
	for token := range branchNameRefusalMarkers {
		assert.Truef(t, seen[token], "no fixture case exercises the %q rule", token)
	}
	// And the accepted half, which is the half that catches a rule refusing
	// MORE than git: `%`, `$`, `]` and the C1 block each shipped as a refusal
	// and each hid a branch the repository already held.
	assert.GreaterOrEqual(t, accepted, 8, "the corpus must pin what the rule ACCEPTS too")
}
