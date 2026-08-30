package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSessionID(t *testing.T) {
	t.Run("accepts valid", func(t *testing.T) {
		assert.NoError(t, ValidateSessionID(""))
		assert.NoError(t, ValidateSessionID("abc-123"))
		assert.NoError(t, ValidateSessionID("session_456"))
		assert.NoError(t, ValidateSessionID("thread-uuid-v4-compat"))
	})

	t.Run("rejects invalid", func(t *testing.T) {
		assert.Error(t, ValidateSessionID("has\"quote"))
		assert.Error(t, ValidateSessionID("has\\backslash"))
		assert.Error(t, ValidateSessionID("has$dollar"))
		assert.Error(t, ValidateSessionID("has%percent"))
		assert.Error(t, ValidateSessionID("has\ttab"))
		assert.Error(t, ValidateSessionID(strings.Repeat("a", 129)))
		// 43 CJK characters is 129 bytes. The length check counts bytes, so
		// it refuses this ID although a character count accepts it.
		assert.Error(t, ValidateSessionID(strings.Repeat("一", 43)))
	})

	t.Run("rejects control characters", func(t *testing.T) {
		// SanitizeName silently strips control chars; ValidateSessionID
		// must reject them because a session ID is an opaque token whose
		// original bytes matter, so silent mutation would confuse the
		// caller (they'd get back a different token than they sent).
		cases := []string{
			"has\x00nul",
			"has\x01soh",
			"has\x1Funitsep",
			"has\x7Fdel",
			"has\x9bcsi",
			"has\nnewline",
			"has\rcarriage",
		}
		for _, id := range cases {
			assert.Errorf(t, ValidateSessionID(id), "expected rejection for %q", id)
		}
	})

	// The guard on the decoupling. This rule used to be defined as
	// "SanitizeName leaves the value unchanged", so relaxing what a NAME may
	// hold would have silently widened what a TOKEN may hold. A session ID
	// becomes an argv element of `claude --resume <id>` and the `sessionId`
	// member of an ACP request, so it must keep refusing what it refused.
	t.Run("stays narrower than the name rule", func(t *testing.T) {
		for _, id := range []string{
			`has"quote`,
			"has\\backslash",
			"has$dollar",
			"has%percent",
		} {
			sanitized, err := SanitizeName(id)
			require.NoErrorf(t, err, "the case only means something while the NAME rule accepts %q", id)
			require.Equalf(t, id, sanitized, "the name rule must return %q unchanged for this case to bite", id)
			assert.Errorf(t, ValidateSessionID(id), "the session ID rule must still refuse %q", id)
		}
	})

	// Every invisible format character is refused, and not U+FEFF alone. Each
	// one travels through a copy and a paste unseen, and a token that carries
	// one names no session the agent knows: the resume then starts a fresh
	// conversation with no report of why.
	t.Run("refuses every invisible format character", func(t *testing.T) {
		for _, r := range []rune{
			0x00AD, 0x061C, 0x180E, 0x200B, 0x200E, 0x200F,
			0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
			0x2060, 0x2066, 0x2067, 0x2068, 0x2069, 0xFEFF,
		} {
			assert.Errorf(t, ValidateSessionID("abc"+string(r)+"123"),
				"the session ID rule must refuse U+%04X in the middle", r)
			assert.Errorf(t, ValidateSessionID(string(r)+"abc-123"),
				"the session ID rule must refuse a leading U+%04X", r)
		}
	})

	// The three characters the NAME rule keeps deliberately. The token class
	// repeats the name rule's list, so it must repeat the exclusions too --
	// a token rule that refused U+200D would be a one-sided widening that
	// nothing else in the repository asked for.
	t.Run("accepts the invisible characters the name rule keeps", func(t *testing.T) {
		for _, r := range []rune{0x200C, 0x200D, 0xFE0F} {
			assert.NoErrorf(t, ValidateSessionID("abc"+string(r)+"123"),
				"U+%04X is kept by the name rule, so the token rule must keep it too", r)
		}
	})

	// A refused character reports the CHARACTER rule, whichever end it sits
	// at. The order of the tests inside ValidateSessionID is what makes that
	// true, and it is why the order is part of the shared fixture: Go's
	// strings.TrimSpace claims U+0085 and JavaScript's trim() claims U+FEFF,
	// so a whitespace test that ran first reported two different messages for
	// one input, one message per language.
	t.Run("reports the character rule and not the whitespace rule at an edge", func(t *testing.T) {
		for _, id := range []string{"\ufeffabc-123", "abc-123\ufeff", "\u0085abc-123", "abc-123\u0085"} {
			err := ValidateSessionID(id)
			require.Errorf(t, err, "expected rejection for %q", id)
			assert.EqualErrorf(t, err, "session ID contains invalid characters",
				"%q must report the character rule, because the two languages trim different sets", id)
		}
	})

	t.Run("refuses whitespace at either end", func(t *testing.T) {
		for _, id := range []string{" abc-123", "abc-123 ", "   ", "\u00a0abc-123", "\u3000abc-123"} {
			err := ValidateSessionID(id)
			require.Errorf(t, err, "expected rejection for %q", id)
			assert.EqualError(t, err, "session ID must not start or end with whitespace")
		}
	})

	// An interior space is ACCEPTED and is not folded. The name rule folds a
	// run of whitespace to one space; a token must not, because the fold
	// resumes a different session.
	t.Run("accepts interior whitespace without folding it", func(t *testing.T) {
		assert.NoError(t, ValidateSessionID("a b"))
		assert.NoError(t, ValidateSessionID("a  b"))
	})

	// The argv rule. A hyphen-prefixed token is not read as the value of
	// `claude --resume`; it parses as a flag of its own, and one argv element
	// reaches `--dangerously-skip-permissions`. Only the FIRST character is
	// refused, because a hyphen anywhere else is ordinary in a UUID and a ULID.
	t.Run("refuses a leading hyphen", func(t *testing.T) {
		for _, id := range []string{
			"-abc-123",
			"--dangerously-skip-permissions",
			"--resume",
			"-",
			"--",
		} {
			err := ValidateSessionID(id)
			require.Errorf(t, err, "expected rejection for %q", id)
			assert.EqualError(t, err, "session ID must not start with a hyphen")
		}
	})

	t.Run("accepts a hyphen anywhere but the first character", func(t *testing.T) {
		for _, id := range []string{
			"abc-123",
			"3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f",
			"abc-123-",
			"a--b",
		} {
			assert.NoErrorf(t, ValidateSessionID(id), "%q must be accepted", id)
		}
	})

	// The hyphen test runs LAST, so an input that breaks two rules reports the
	// earlier one. The two languages pin this order through the shared fixture:
	// without it, one side reports the hyphen and the other the whitespace, and
	// the browser refuses a token the worker accepts.
	t.Run("reports an earlier rule than the hyphen when both apply", func(t *testing.T) {
		err := ValidateSessionID("-abc ")
		require.Error(t, err)
		assert.EqualError(t, err, "session ID must not start or end with whitespace")

		err = ValidateSessionID("-abc\x00")
		require.Error(t, err)
		assert.EqualError(t, err, "session ID contains invalid characters")

		// A leading SPACE beats the hyphen that follows it, because the first
		// rune is what each edge test reads.
		err = ValidateSessionID(" -abc")
		require.Error(t, err)
		assert.EqualError(t, err, "session ID must not start or end with whitespace")
	})

	t.Run("accepts the byte limit and refuses one byte past it", func(t *testing.T) {
		assert.NoError(t, ValidateSessionID(strings.Repeat("a", SessionIDByteLimit)))
		err := ValidateSessionID(strings.Repeat("a", SessionIDByteLimit+1))
		require.Error(t, err)
		// The message names the field once. It read "invalid session ID:
		// session ID must be at most 128 bytes" before, where the caller adds
		// no prefix of its own and the two sibling messages carry none.
		assert.EqualError(t, err, "session ID must be at most 128 bytes")
	})
}

// TestValidateSessionIDRefusesInvalidUTF8 pins the half of the rule that the
// shared fixture cannot carry: encoding/json rewrites an unpaired surrogate
// escape to U+FFFD, so no JSON file can hold an invalid-UTF-8 input.
//
// The rule tests utf8.ValidString FIRST, and it must. `for range` over a Go
// string yields U+FFFD for an invalid byte, and U+FFFD is not a control
// character, not an invisible format character, and none of the four
// punctuation marks -- so a character loop alone let the byte through, and it
// reached `claude --resume` where it names no session.
func TestValidateSessionIDRefusesInvalidUTF8(t *testing.T) {
	for _, id := range []string{"abc\xffdef", "\xff", "abc\xed\xa0\x80def", "\xc0\x80"} {
		err := ValidateSessionID(id)
		require.Errorf(t, err, "expected rejection for %q", id)
		assert.EqualErrorf(t, err, "session ID must be valid UTF-8",
			"%q must report the UTF-8 rule", id)
	}

	// A U+FFFD the caller sent deliberately is valid UTF-8 and ordinary text,
	// so the rule accepts it. That is what makes the case above a real test:
	// the rule tells the two apart by the ENCODING, not by the rune.
	assert.NoError(t, ValidateSessionID("abc\ufffddef"))
}

// sessionIDConformanceFixture mirrors testdata/session_id_conformance.json.
// The TypeScript suite reads the same file; see the file's own _readme for the
// contract and for why the invalid-UTF-8 case cannot live there.
type sessionIDConformanceFixture struct {
	Cases []struct {
		Input   sessionIDConformanceSpec `json:"input"`
		Valid   bool                     `json:"valid"`
		Refusal string                   `json:"refusal"`
		Why     string                   `json:"why"`
	} `json:"cases"`
}

type sessionIDConformanceSpec struct {
	Text string `json:"text"`
	// Repeat is a POINTER so that an explicit `"repeat": 0` is told apart
	// from an absent field. Go's zero value cannot tell them apart, and the
	// TypeScript loader reads `spec.repeat ?? 1`, which honours an explicit
	// 0 -- so a plain int here made the two languages build different inputs
	// from one case, and the failure read as a rule divergence.
	Repeat *int   `json:"repeat"`
	Tail   string `json:"tail"`
}

func (s sessionIDConformanceSpec) build() string {
	repeat := 1
	if s.Repeat != nil {
		repeat = *s.Repeat
	}
	return strings.Repeat(s.Text, repeat) + s.Tail
}

// sessionIDRefusalMarkers maps a fixture refusal token to a substring of this
// package's message for that rule. The browser's messages differ by the
// leading capital only, so its suite carries its own map.
var sessionIDRefusalMarkers = map[string]string{
	"too_long":            "must be at most",
	"not_utf8":            "must be valid UTF-8",
	"forbidden_character": "contains invalid characters",
	"whitespace_at_edge":  "must not start or end with whitespace",
	"leading_hyphen":      "must not start with a hyphen",
}

func TestValidateSessionIDConformance(t *testing.T) {
	const path = "../../../testdata/session_id_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/validate.test.ts")

	var fixture sessionIDConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass
	// while asserting nothing -- the one failure mode a shared fixture must
	// not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)

	for _, c := range fixture.Cases {
		t.Run(c.Why, func(t *testing.T) {
			err := ValidateSessionID(c.Input.build())
			if c.Valid {
				require.Emptyf(t, c.Refusal, "case %q is valid, so its refusal must be empty", c.Why)
				assert.NoErrorf(t, err, "case %q must be accepted", c.Why)
				return
			}
			require.Errorf(t, err, "case %q must be refused", c.Why)
			marker, ok := sessionIDRefusalMarkers[c.Refusal]
			require.Truef(t, ok, "case %q carries an unknown refusal token %q", c.Why, c.Refusal)
			assert.Containsf(t, err.Error(), marker,
				"case %q must report the %s rule", c.Why, c.Refusal)
		})
	}
}
