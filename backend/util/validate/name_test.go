package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeName(t *testing.T) {
	t.Run("returns sanitized name", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			want  string
		}{
			{"simple", "hello", "hello"},
			{"with spaces", "hello world", "hello world"},
			{"with hyphens", "my-name", "my-name"},
			{"with underscores", "my_name", "my_name"},
			{"with dots", "my.name", "my.name"},
			{"with numbers", "name123", "name123"},
			{"mixed", "My Name-1.0_beta", "My Name-1.0_beta"},
			{"special chars @", "name@here", "name@here"},
			{"special chars !", "hello!", "hello!"},
			{"special chars /", "path/name", "path/name"},
			{"special chars '", "it's fine", "it's fine"},
			{"special chars +", "a + b = c", "a + b = c"},
			{"special chars parens", "project (draft)", "project (draft)"},
			{"unicode", "café", "café"},
			{"emoji", "hello\U0001F600", "hello\U0001F600"},
			{"128 ASCII bytes", strings.Repeat("a", 128), strings.Repeat("a", 128)},
			{"trims leading/trailing spaces", "  hello  ", "hello"},
			// Forbidden chars are silently stripped
			{"strips double quotes", `name"quoted`, "namequoted"},
			{"strips backslashes", "back\\slash", "backslash"},
			{"strips tabs", "hello\tworld", "helloworld"},
			{"strips newlines", "hello\nworld", "helloworld"},
			{"strips control chars", "hello\x00world", "helloworld"},
			{"strips 0x1F", "hello\x1Fworld", "helloworld"},
			{"strips 0x7F", "hello\x7Fworld", "helloworld"},
			{"strips dollar", "hello$world", "helloworld"},
			{"strips percent", "100%done", "100done"},
			// U+FEFF is category Cf, so unicode.IsControl reports false for
			// it. It is stripped anyway: JavaScript's trim() removes it and
			// Go's TrimSpace keeps it, so a pasted byte order mark was the one
			// input on which this rule and its browser copy disagreed.
			{"strips the byte order mark", "\ufeffhello\ufeffworld", "helloworld"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := SanitizeName(tt.input)
				require.NoError(t, err, "SanitizeName(%q) should not return error", tt.input)
				assert.Equal(t, tt.want, got, "SanitizeName(%q) sanitized result", tt.input)
			})
		}
	})

	t.Run("returns error", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
		}{
			{"empty", ""},
			{"whitespace only", "   "},
			{"too long", strings.Repeat("a", 129)},
			{"only forbidden chars", string(make([]byte, 64))},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := SanitizeName(tt.input)
				assert.Error(t, err, "SanitizeName(%q) should return error", tt.input)
			})
		}
	})

	// The limit counts UTF-8 bytes, not characters. A byte count and a rune
	// count agree on ASCII and disagree on every wider script, so an ASCII
	// boundary alone cannot tell the two rules apart. The browser copy in
	// `frontend/src/lib/validate.ts` pins the same two CJK cases. Both sides
	// must refuse the same name. A client that accepts a name that this
	// function then refuses gives the user a failure with no reason at the
	// field.
	t.Run("counts UTF-8 bytes, not characters", func(t *testing.T) {
		const cjk = "\u4e00" // 3 UTF-8 bytes

		t.Run("accepts 128 ASCII letters", func(t *testing.T) {
			input := strings.Repeat("a", 128)
			require.Len(t, input, 128, "128 ASCII letters is 128 bytes")
			got, err := SanitizeName(input)
			require.NoError(t, err)
			assert.Equal(t, input, got)
		})

		t.Run("refuses 129 ASCII letters", func(t *testing.T) {
			input := strings.Repeat("a", 129)
			require.Len(t, input, 129, "129 ASCII letters is 129 bytes")
			_, err := SanitizeName(input)
			require.Error(t, err)
			assert.EqualError(t, err, "name must be at most 128 bytes")
		})

		t.Run("accepts 42 CJK characters", func(t *testing.T) {
			input := strings.Repeat(cjk, 42)
			require.Len(t, input, 126, "42 CJK characters is 126 bytes")
			got, err := SanitizeName(input)
			require.NoError(t, err)
			assert.Equal(t, input, got)
		})

		t.Run("refuses 43 CJK characters", func(t *testing.T) {
			// 43 characters, which a character count accepts. 129 bytes,
			// which this limit refuses.
			input := strings.Repeat(cjk, 43)
			require.Len(t, input, 129, "43 CJK characters is 129 bytes")
			require.Equal(t, 43, utf8.RuneCountInString(input))
			_, err := SanitizeName(input)
			require.Error(t, err)
			assert.EqualError(t, err, "name must be at most 128 bytes")
		})
	})
}

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
		assert.Error(t, ValidateSessionID(strings.Repeat("\u4e00", 43)))
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
			"has\nnewline",
			"has\rcarriage",
		}
		for _, id := range cases {
			assert.Errorf(t, ValidateSessionID(id), "expected rejection for %q", id)
		}
	})
}

func TestCleanName(t *testing.T) {
	t.Run("keeps a name SanitizeName already accepts", func(t *testing.T) {
		for _, input := range []string{
			"hello",
			"My Name-1.0_beta",
			"café",
			strings.Repeat("a", NameByteLimit),
		} {
			assert.Equal(t, input, CleanName(input))
		}
	})

	t.Run("cuts to the byte limit instead of failing", func(t *testing.T) {
		// The rune count and the byte count answer differently here: 50 CJK
		// characters is 50 characters and 150 bytes. SanitizeName refuses this
		// name; CleanName cuts it to the 42 characters that fit.
		input := strings.Repeat("一", 50)
		require.Len(t, input, 150)
		_, err := SanitizeName(input)
		require.Error(t, err, "the case only means something while SanitizeName refuses it")

		got := CleanName(input)
		assert.Equal(t, strings.Repeat("一", 42), got)
		assert.LessOrEqual(t, len(got), NameByteLimit)
		assert.True(t, utf8.ValidString(got), "the cut must land on a rune boundary")
	})

	t.Run("returns empty when nothing survives", func(t *testing.T) {
		for _, input := range []string{
			"",
			"   ",
			"$$%%",
			"\x00\x01\x7f",
			strings.Repeat("%", 200),
		} {
			assert.Empty(t, CleanName(input), "CleanName(%q)", input)
		}
	})

	// The property that makes CleanName safe to apply at more than one point:
	// a title cleaned in the browser and cleaned again in the worker is the
	// title the browser showed.
	t.Run("is idempotent", func(t *testing.T) {
		for _, input := range []string{
			"hello",
			"  spaced  ",
			"a\"b\\c$d%e",
			strings.Repeat("一", 50),
			strings.Repeat("a", 200),
			"\ufeffPlan",
			"",
		} {
			once := CleanName(input)
			assert.Equal(t, once, CleanName(once), "CleanName(%q) must not change on a second pass", input)
		}
	})

	// SanitizeName reports "too long" only for input it has not cut. CleanName
	// cuts first, so that error is unreachable and every non-empty result is a
	// name SanitizeName returns unchanged.
	t.Run("returns a name SanitizeName accepts unchanged", func(t *testing.T) {
		for _, input := range []string{
			strings.Repeat("a", 500),
			strings.Repeat("一", 500),
			strings.Repeat("\U0001F600", 100),
			"  \tlots of \x00junk\x1f  " + strings.Repeat("한", 200),
		} {
			got := CleanName(input)
			require.NotEmpty(t, got)
			sanitized, err := SanitizeName(got)
			require.NoErrorf(t, err, "SanitizeName must accept CleanName(%q)", input)
			assert.Equal(t, got, sanitized)
		}
	})
}

func TestTruncateToBytes(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{name: "shorter than the limit", s: "abc", limit: 10, want: "abc"},
		{name: "exactly the limit", s: "abc", limit: 3, want: "abc"},
		{name: "one byte over", s: "abcd", limit: 3, want: "abc"},
		{name: "cut inside a rune moves back", s: "한한", limit: 4, want: "한"},
		{name: "cut on a rune boundary", s: "한한", limit: 3, want: "한"},
		{name: "no whole rune fits", s: "한", limit: 2, want: ""},
		{name: "cut inside a 4-byte rune", s: "a\U0001F600", limit: 4, want: "a"},
		{name: "zero limit", s: "abc", limit: 0, want: ""},
		{name: "negative limit", s: "abc", limit: -1, want: ""},
		{name: "empty string", s: "", limit: 8, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateToBytes(tt.s, tt.limit)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "the result must stay valid UTF-8")
		})
	}
}

// titleConformanceSpec builds one string of the shared fixture: Text repeated
// Repeat times, then Tail. A spec keeps a boundary readable as the NUMBER 128
// rather than as 128 characters a reader has to count. An absent Repeat
// decodes as 0 and means 1, matching the fixture's stated default.
type titleConformanceSpec struct {
	Text   string `json:"text"`
	Repeat int    `json:"repeat"`
	Tail   string `json:"tail"`
}

func (s titleConformanceSpec) build() string {
	repeat := s.Repeat
	if repeat == 0 {
		repeat = 1
	}
	return strings.Repeat(s.Text, repeat) + s.Tail
}

// titleConformanceFixture is the shared cross-language fixture described in
// testdata/title_cleaning_conformance.json -- read that file's _readme first.
//
// frontend/src/lib/validate.ts hand-mirrors this package's rule so the browser
// can show the cleaned title the worker will store. This side of the fixture
// pins the worker's half: change what CleanName does without changing
// validate.ts, and the TypeScript suite reading the same file goes red (and the
// reverse). Fields mirror the JSON exactly; Why is documentation, not asserted
// on.
type titleConformanceFixture struct {
	Cases []struct {
		Input   titleConformanceSpec `json:"input"`
		Cleaned titleConformanceSpec `json:"cleaned"`
		Why     string               `json:"why"`
	} `json:"cases"`
}

// TestCleanNameConformance runs the shared fixture.
//
// go test runs with CWD = the package dir (backend/util/validate), so the
// repo-level testdata dir -- the one home reachable from both the Go package
// and frontend/src/lib -- is three levels up.
func TestCleanNameConformance(t *testing.T) {
	const path = "../../../testdata/title_cleaning_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/validate.test.ts")

	var fixture titleConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass while
	// asserting nothing -- the one failure mode a shared fixture must not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)

	for _, c := range fixture.Cases {
		t.Run(c.Why, func(t *testing.T) {
			input := c.Input.build()
			want := c.Cleaned.build()

			got := CleanName(input)
			assert.Equalf(t, want, got, "case %q", c.Why)
			assert.Equalf(t, want, CleanName(got), "case %q must be idempotent", c.Why)
			assert.Truef(t, utf8.ValidString(got), "case %q must stay valid UTF-8", c.Why)
			assert.LessOrEqualf(t, len(got), NameByteLimit, "case %q must fit the byte limit", c.Why)
		})
	}
}
