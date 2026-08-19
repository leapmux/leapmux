package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
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
			// The four characters this rule used to strip. Each one is
			// ordinary text: no sink reads a stored name as syntax, and the
			// ban only removed what the user typed.
			{"keeps a double quote", `name"quoted`, `name"quoted`},
			{"keeps a backslash", "back\\slash", "back\\slash"},
			{"keeps a dollar", "hello$world", "hello$world"},
			{"keeps a percent", "100%done", "100%done"},
			{"keeps a whole shell command", `npm test --grep "$FOO"`, `npm test --grep "$FOO"`},
			{"keeps a Windows path", `C:\Users\me`, `C:\Users\me`},
			// A control character is stripped, unless it is also whitespace.
			{"strips control chars", "hello\x00world", "helloworld"},
			{"strips 0x1F", "hello\x1Fworld", "helloworld"},
			{"strips 0x7F", "hello\x7Fworld", "helloworld"},
			// Whitespace FOLDS rather than vanishing, so a pasted two-line
			// title does not run its two lines together.
			{"folds a tab to one space", "hello\tworld", "hello world"},
			{"folds a newline to one space", "hello\nworld", "hello world"},
			{"folds a carriage return to one space", "hello\r\nworld", "hello world"},
			{"folds a run of spaces to one", "hello    world", "hello world"},
			{"folds a no-break space to a plain space", "hello\u00a0world", "hello world"},
			// U+0085 is Cc AND whitespace. Go's unicode.IsSpace claims it and
			// JavaScript's \s does not, which is why the browser copy spells
			// it out in its own fold set.
			{"folds the next-line control to one space", "hello\u0085world", "hello world"},
			// The invisible format characters. A reader cannot see one, so it
			// can only hide text or pad a name past a limit the visible
			// characters fit.
			{"strips the byte order mark", "\ufeffhello\ufeffworld", "helloworld"},
			{"strips a zero width space", "hello\u200bworld", "helloworld"},
			{"strips a soft hyphen", "hello\u00adworld", "helloworld"},
			{"strips the word joiner", "hello\u2060world", "helloworld"},
			{"strips a right-to-left override", "\u202etxt.exe", "txt.exe"},
			{"strips the bidi isolates", "\u2066hello\u2069world", "helloworld"},
			{"strips an Arabic letter mark", "hello\u061cworld", "helloworld"},
			{"strips a Mongolian vowel separator", "hello\u180eworld", "helloworld"},
			// Kept, although they are invisible too: removing one rewrites the
			// text rather than tidying it.
			{"keeps the zero width joiner", "\U0001f468\u200d\U0001f469", "\U0001f468\u200d\U0001f469"},
			{"keeps the zero width non-joiner", "\u0915\u094d\u200c\u0937", "\u0915\u094d\u200c\u0937"},
			{"keeps a variation selector", "love \u2764\ufe0f", "love \u2764\ufe0f"},
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

// TestSanitizeNameScanLimit pins the one hazard that CleanNameChars' early
// stop introduces. The loop stops APPENDING once the builder holds
// cleanScanLimit bytes, so a title that a provider reported as a whole log
// line costs a bounded allocation. The stop bounds the OUTPUT and not the
// scan: an input of nothing but stripped characters never grows the builder,
// so the loop still reads every byte of it.
//
// The stop must be one byte PAST NameByteLimit: at exactly the limit, an
// over-long name would stop while it still measured 128 bytes, and
// SanitizeName would ACCEPT it.
//
// The 129-byte cases in TestSanitizeName already fail on that off-by-one. These
// cases state the reason, and they cover the two directions the stop can be
// wrong in for an input far larger than the limit.
func TestSanitizeNameScanLimit(t *testing.T) {
	t.Run("still refuses a name far past the limit", func(t *testing.T) {
		for _, input := range []string{
			strings.Repeat("a", 100_000),
			strings.Repeat("\u4e00", 100_000),
			strings.Repeat("a", NameByteLimit) + strings.Repeat("b", 100_000),
			strings.Repeat("a", NameByteLimit) + " b",
		} {
			_, err := SanitizeName(input)
			assert.Errorf(t, err, "SanitizeName must refuse a %d-byte input", len(input))
		}
	})

	// The mirror hazard: the stop must not cut a name whose CONTENT fits. Every
	// byte past the limit here is stripped or trimmed, so the true result is
	// exactly NameByteLimit and the name is valid.
	t.Run("still accepts a name whose content fits after a long stripped tail", func(t *testing.T) {
		fits := strings.Repeat("a", NameByteLimit)
		for _, input := range []string{
			fits + strings.Repeat("\u200b", 10_000),
			fits + strings.Repeat("\x00", 10_000),
			fits + strings.Repeat(" ", 10_000),
			strings.Repeat("\u200b", 10_000) + fits,
		} {
			got, err := SanitizeName(input)
			require.NoErrorf(t, err, "SanitizeName must accept an input whose content is %d bytes", NameByteLimit)
			assert.Equal(t, fits, got)
		}
	})

	// CleanName reads the same stopped builder, so a stop that fired early must
	// not change the 128 bytes it keeps.
	t.Run("cuts a huge input to the same bytes a full scan would keep", func(t *testing.T) {
		for _, tc := range []struct{ input, want string }{
			{strings.Repeat("a", 100_000), strings.Repeat("a", NameByteLimit)},
			{strings.Repeat("\u4e00", 100_000), strings.Repeat("\u4e00", 42)},
			{strings.Repeat("\U0001f600", 100_000), strings.Repeat("\U0001f600", 32)},
		} {
			got := CleanName(tc.input)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), NameByteLimit)
		}
	})
}

// SanitizeDisplayName is the only caller that substitutes a fallback, and both
// its branches run the relaxed rule. A display name reaches users.display_name
// on six paths, one of which takes the name an OAuth provider reports.
func TestSanitizeDisplayName(t *testing.T) {
	t.Run("uses the display name when it is not empty", func(t *testing.T) {
		got, err := SanitizeDisplayName("Ada Lovelace", "ada")
		require.NoError(t, err)
		assert.Equal(t, "Ada Lovelace", got)
	})

	t.Run("falls back only on the EMPTY string, not on one that cleans to empty", func(t *testing.T) {
		got, err := SanitizeDisplayName("", "ada")
		require.NoError(t, err)
		assert.Equal(t, "ada", got, "the empty display name takes the fallback")

		// A name that cleaning empties does NOT reach the fallback: the check
		// runs on the RAW value. The caller gets the error instead, which is
		// what a field whose error a user reads and corrects wants.
		_, err = SanitizeDisplayName("\u200b\ufeff", "ada")
		assert.Error(t, err, "a name that cleans to empty is refused, not replaced")
	})

	t.Run("applies the relaxed rule to both branches", func(t *testing.T) {
		got, err := SanitizeDisplayName(`Ada "100%" O$Brien`, "ada")
		require.NoError(t, err)
		assert.Equal(t, `Ada "100%" O$Brien`, got, "punctuation survives in a display name")

		got, err = SanitizeDisplayName("  Ada \t Lovelace  ", "ada")
		require.NoError(t, err)
		assert.Equal(t, "Ada Lovelace", got, "the whitespace folds and trims")

		got, err = SanitizeDisplayName("", "  Grace \n Hopper  ")
		require.NoError(t, err)
		assert.Equal(t, "Grace Hopper", got, "the FALLBACK is cleaned too")
	})

	t.Run("refuses an over-long name and an over-long fallback", func(t *testing.T) {
		_, err := SanitizeDisplayName(strings.Repeat("a", NameByteLimit+1), "ada")
		assert.Error(t, err)
		_, err = SanitizeDisplayName("", strings.Repeat("a", NameByteLimit+1))
		assert.Error(t, err, "an over-long fallback is refused, not cut")
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
			"\x00\x01\x7f",
			"\ufeff\u200b\u00ad\u2060",
			strings.Repeat("\u200b", 200),
			"\t\n\r  \u00a0",
		} {
			assert.Empty(t, CleanName(input), "CleanName(%q)", input)
		}
	})

	// The four characters this rule used to strip now reach the row. The old
	// rule emptied each of these, and a caller's fallback label replaced a
	// title the user typed.
	t.Run("keeps a title made only of the characters the old rule stripped", func(t *testing.T) {
		for _, tc := range []struct{ input, want string }{
			{"$$%%", "$$%%"},
			{`npm test --grep "$FOO"`, `npm test --grep "$FOO"`},
			{"50% \\ $HOME", "50% \\ $HOME"},
			{strings.Repeat("%", 200), strings.Repeat("%", 128)},
		} {
			assert.Equal(t, tc.want, CleanName(tc.input), "CleanName(%q)", tc.input)
		}
	})

	// The order the rule chose: CLEAN FIRST, CUT SECOND. A cut-first rule
	// spent its whole budget on characters it was about to remove, and
	// returned the empty string for a title that holds one.
	t.Run("cleans before it cuts, so a long stripped prefix does not remove the title", func(t *testing.T) {
		assert.Equal(t, "Plan", CleanName(strings.Repeat("\u200b", 130)+"Plan"))
		assert.Equal(t, "Plan", CleanName(strings.Repeat("\x00", 500)+"Plan"))
		assert.Equal(t, "Plan the migration", CleanName(strings.Repeat(" ", 300)+"Plan\nthe\tmigration"))
	})

	// The fold is what keeps two pasted lines apart. Stripping the newline
	// instead ran the last word of one line into the first of the next.
	t.Run("folds whitespace instead of dropping it", func(t *testing.T) {
		assert.Equal(t, "Fix parser Add tests", CleanName("Fix parser\nAdd tests"))
		assert.Equal(t, "Fix parser Add tests", CleanName("  Fix   parser \t\n Add\u00a0tests  "))
	})

	// The property that makes CleanName safe to apply at more than one point:
	// a title cleaned in the browser and cleaned again in the worker is the
	// title the browser showed.
	t.Run("is idempotent", func(t *testing.T) {
		for _, input := range []string{
			"hello",
			"  spaced  ",
			"a\"b\\c$d%e",
			"Fix parser\nAdd tests",
			"  lots \t of \u00a0 whitespace  ",
			strings.Repeat("一", 50),
			strings.Repeat("a", 200),
			strings.Repeat("%", 200),
			"\ufeffPlan",
			"\u202ePlan\u200b",
			"",
		} {
			once := CleanName(input)
			assert.Equal(t, once, CleanName(once), "CleanName(%q) must not change on a second pass", input)
		}
	})

	// CleanName cuts LAST, so its result fits the byte limit by construction
	// and SanitizeName's "too long" error is unreachable from it. Every
	// non-empty result is therefore a name SanitizeName returns unchanged.
	t.Run("returns a name SanitizeName accepts unchanged", func(t *testing.T) {
		for _, input := range []string{
			strings.Repeat("a", 500),
			strings.Repeat("一", 500),
			strings.Repeat("\U0001F600", 100),
			"  \tlots of \x00junk\x1f  " + strings.Repeat("한", 200),
			strings.Repeat("%", 500),
			strings.Repeat("\u200b", 200) + strings.Repeat("$", 200),
		} {
			got := CleanName(input)
			require.NotEmpty(t, got)
			sanitized, err := SanitizeName(got)
			require.NoErrorf(t, err, "SanitizeName must accept CleanName(%q)", input)
			assert.Equal(t, got, sanitized)
		}
	})
}

// TestCleanNameKeepsTagSequence pins a KEEP that both implementations promise
// in prose and that nothing asserted.
//
// invisibleFormat holds no R32 entry, so `unicode.Is` reports false for every
// astral rune, and the browser class is BMP-only. Both sides therefore keep
// the tag characters U+E0020-U+E007F by accident of construction rather than
// by assertion: add an R32 entry, or move either side to a `Cf` category test,
// and the flag of Scotland breaks into a bare black flag in every tab title
// while every other test stays green.
func TestCleanNameKeepsTagSequence(t *testing.T) {
	// U+1F3F4 WAVING BLACK FLAG, then the tag characters that spell "gbsct",
	// then U+E007F CANCEL TAG. This is the flag of Scotland.
	const scotland = "\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"

	assert.Equal(t, "Tab "+scotland, CleanName("Tab "+scotland),
		"a tag sequence must survive whole; a broken one renders as a bare black flag")
	assert.False(t, IsUnreadable(0xE0067), "a tag character must not be unreadable")
	assert.False(t, IsUnreadable(0xE007F), "the cancel tag must not be unreadable")
	// The variation selectors and the joiners take the same answer, and the
	// fixture covers them; these two assertions state the rule for the astral
	// range that the fixture cannot reach through JSON escapes alone.
	assert.False(t, IsUnreadable(0xFE0F), "a variation selector must not be unreadable")
	assert.False(t, IsUnreadable(0x200D), "the zero width joiner must not be unreadable")
}

// TestCleanNameLeavesNoControlCharacter walks EVERY code point in the two
// control blocks, rather than one representative per block.
//
// The browser copy encodes "control minus whitespace" as two hand-punched
// holes in a literal range list (`\x00-\x08\x0E-\x1F` and
// `\x7F-\x84\x86-\x9F`), and this side computes the same overlap by
// testing whitespace before control. A one-sided edit to either fold set turns
// a hole into a leak, and a control character then reaches a stored name.
// U+000B and U+000C had no case in either suite before this test.
func TestCleanNameLeavesNoControlCharacter(t *testing.T) {
	for r := rune(0); r <= 0x9F; r++ {
		if r >= 0x20 && r < 0x7F {
			continue // printable ASCII, which the rule keeps
		}
		got := CleanName("a" + string(r) + "b")
		// Either the character vanished, or it folded to one space. Nothing
		// else is a correct answer, and the character itself must never
		// survive.
		assert.Containsf(t, []string{"ab", "a b"}, got,
			"CleanName must strip or fold U+%04X, got %q", r, got)
		assert.NotContainsf(t, got, string(r), "U+%04X survived into the result", r)
	}

	// The mirror: every printable ASCII character survives untouched, so the
	// loop above cannot pass by stripping too much.
	for r := rune(0x20); r < 0x7F; r++ {
		want := "a" + string(r) + "b"
		if r == ' ' {
			want = "a b"
		}
		assert.Equalf(t, want, CleanName("a"+string(r)+"b"),
			"CleanName must keep the printable character U+%04X", r)
	}
}

// TestCleanNameDropsInvalidBytes pins the half of the rule that
// testdata/title_cleaning_conformance.json cannot carry: encoding/json rewrites
// an unpaired \uD800 escape to U+FFFD, so the fixture cannot state an input
// that is not valid UTF-8. The browser suite pins its own half with a lone
// surrogate.
//
// `for range` over a string decodes each invalid byte as U+FFFD, which is 3
// bytes out for 1 byte in. That was the one way the character rule could GROW a
// string, and it is why a title of mostly invalid bytes used to be discarded
// whole: the grown string failed the length check and CleanName returned the
// empty string for it.
func TestCleanNameDropsInvalidBytes(t *testing.T) {
	t.Run("drops an invalid byte and keeps the text around it", func(t *testing.T) {
		got := CleanName("Pl\xffan")
		assert.Equal(t, "Plan", got)
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("keeps a replacement character the caller sent deliberately", func(t *testing.T) {
		// A real U+FFFD decodes with size 3, so the invalid-byte branch must
		// not swallow it.
		assert.Equal(t, "Pl\ufffdan", CleanName("Pl\ufffdan"))
	})

	t.Run("cuts an all-invalid title instead of discarding it", func(t *testing.T) {
		got := CleanName(strings.Repeat("\xff", 200) + "Plan")
		assert.Equal(t, "Plan", got, "the invalid bytes go, and the title they hid survives")
	})

	t.Run("keeps every result inside the byte limit", func(t *testing.T) {
		for _, input := range []string{
			strings.Repeat("\xff", 200),
			strings.Repeat("\xff", 200) + strings.Repeat("a", 200),
			strings.Repeat("a", 100) + strings.Repeat("\xfe", 100),
		} {
			got := CleanName(input)
			assert.LessOrEqualf(t, len(got), NameByteLimit, "CleanName(%q) must fit the limit", input)
			assert.Truef(t, utf8.ValidString(got), "CleanName(%q) must stay valid UTF-8", input)
			if got != "" {
				sanitized, err := SanitizeName(got)
				require.NoError(t, err)
				assert.Equal(t, got, sanitized)
			}
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
// rather than as 128 characters a reader has to count. An absent Repeat means
// 1, matching the fixture's stated default.
//
// Repeat is a POINTER so that an explicit `"repeat": 0` is told apart from an
// absent field. Go's zero value cannot tell them apart, and the TypeScript
// loader reads `spec.repeat ?? 1`, which honours an explicit 0 -- so a plain
// int here made the two languages build DIFFERENT inputs from one case, and
// the failure read as a drift in the cleaning rule rather than in the loaders.
type titleConformanceSpec struct {
	Text   string `json:"text"`
	Repeat *int   `json:"repeat"`
	Tail   string `json:"tail"`
}

func (s titleConformanceSpec) build() string {
	repeat := 1
	if s.Repeat != nil {
		repeat = *s.Repeat
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

// TestNameWhitespaceMatchesUnicode reports the day the pinned fold set and
// Go's own White_Space table stop agreeing.
//
// nameWhitespace is written out by code point so that a Go upgrade cannot move
// the fold set out from under the browser copy. The pin costs staleness: a
// Space_Separator added to Unicode later is NOT folded, and renders as a
// visible character inside a title, until somebody adds it here. This test is
// what makes that a decision rather than an accident.
//
// A failure here is NOT automatically a bug. It means Go's table moved, and a
// human must decide whether the name rule adopts the new character -- in BOTH
// languages, together with a case in the shared fixture.
func TestNameWhitespaceMatchesUnicode(t *testing.T) {
	var extra, missing []rune
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // a surrogate is not a character
		}
		switch {
		case unicode.Is(nameWhitespace, r) && !unicode.IsSpace(r):
			extra = append(extra, r)
		case !unicode.Is(nameWhitespace, r) && unicode.IsSpace(r):
			missing = append(missing, r)
		}
	}
	assert.Emptyf(t, extra, "the pinned fold set holds %U, which Go's unicode.IsSpace no longer claims", extra)
	assert.Emptyf(t, missing, "Go's unicode.IsSpace claims %U, which the pinned fold set does not hold", missing)
}

// TestNameWhitespaceIsNotUnreadable pins the split between the two pinned
// tables. A character cannot be both folded and stripped, and CleanNameChars
// asks the fold first, so an overlap would make the strip arm unreachable for
// that character and give the two languages different answers for it.
func TestNameWhitespaceIsNotUnreadable(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if unicode.Is(nameWhitespace, r) && unicode.Is(invisibleFormat, r) {
			t.Errorf("U+%04X is in BOTH the fold set and the invisible set", r)
		}
	}
}

// TestStripUnreadable covers the exported helper directly. The OSC
// notification body reads it, and it wants the half of the name rule that a
// non-name value needs: the strip and the cap, without the fold and without
// the trim. A registry row key read it too until that key became an identity
// that is refused rather than rewritten (bgtask.ValidateRowKey), so a body is
// the only caller now -- a body is PROSE, and rewriting prose loses nothing
// that a later lookup depends on.
func TestStripUnreadable(t *testing.T) {
	t.Run("strips what a reader cannot see", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"a\x00b", "ab"},   // C0
			{"a\x1fb", "ab"},   // top of C0
			{"a\x7fb", "ab"},   // DEL
			{"a\u009fb", "ab"}, // C1
			{"a\nb", "ab"},     // a whitespace control is control too
			{"a\u200bb", "ab"}, // zero width space
			{"a\u00adb", "ab"}, // soft hyphen
			{"a\u202eb", "ab"}, // right-to-left override
			{"a\ufeffb", "ab"}, // byte order mark
			{"a\u2060b", "ab"}, // word joiner
		} {
			assert.Equalf(t, tc.want, StripUnreadable(tc.in, 0), "input %q", tc.in)
		}
	})

	t.Run("keeps what the name rule keeps", func(t *testing.T) {
		// Visible punctuation, and the three invisible characters that the
		// name rule keeps deliberately. One definition of "unreadable" means
		// this helper cannot answer differently from CleanNameChars about
		// which characters a reader can see.
		for _, s := range []string{
			`100% of $HOME "quoted" c:\path`,
			"\U0001f468\u200d\U0001f469",
			"love \u2764\ufe0f",
			"\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f",
		} {
			assert.Equalf(t, s, StripUnreadable(s, 0), "input %q must survive whole", s)
		}
	})

	// It does NOT fold and does NOT trim. A key must keep the bytes that tell
	// two keys apart, and a message keeps the sender's formatting.
	t.Run("neither folds a whitespace run nor trims the ends", func(t *testing.T) {
		assert.Equal(t, "  a   b  ", StripUnreadable("  a   b  ", 0))
		assert.Equal(t, "\u00a0a\u3000b", StripUnreadable("\u00a0a\u3000b", 0))
	})

	t.Run("drops an invalid byte instead of growing it", func(t *testing.T) {
		got := StripUnreadable("a\xffb", 0)
		assert.Equal(t, "ab", got)
		assert.True(t, utf8.ValidString(got))
		// A U+FFFD the caller sent decodes with size 3 and is ordinary text.
		assert.Equal(t, "a\ufffdb", StripUnreadable("a\ufffdb", 0))
		// The result never grows: 10 invalid bytes used to become 30.
		in := strings.Repeat("\xff", 10)
		assert.LessOrEqual(t, len(StripUnreadable(in, 0)), len(in))
	})

	t.Run("cuts at the byte limit on a rune boundary", func(t *testing.T) {
		assert.Equal(t, "abc", StripUnreadable("abcdef", 3))
		assert.Equal(t, "abcdef", StripUnreadable("abcdef", 100))
		// The limit is not a multiple of 3, so it lands inside the third rune
		// and the cut moves back. A partial rune is invalid UTF-8, which
		// fails the proto broadcast marshal.
		got := StripUnreadable(strings.Repeat("\u4e00", 10), 8)
		assert.True(t, utf8.ValidString(got))
		assert.Equal(t, strings.Repeat("\u4e00", 2), got)
		// A limit of zero or less applies no cut.
		assert.Equal(t, "abcdef", StripUnreadable("abcdef", 0))
		assert.Equal(t, "abcdef", StripUnreadable("abcdef", -1))
	})

	// The cap counts the KEPT bytes, not the input bytes, so a value padded
	// with invisible characters does not lose the text behind them. This is
	// the same clean-first property CleanName has.
	t.Run("counts the kept bytes and not the input bytes", func(t *testing.T) {
		assert.Equal(t, "abc", StripUnreadable(strings.Repeat("\u200b", 1000)+"abc", 3))
	})

	t.Run("handles the empty input", func(t *testing.T) {
		assert.Empty(t, StripUnreadable("", 0))
		assert.Empty(t, StripUnreadable("", 10))
		assert.Empty(t, StripUnreadable("\u200b\x00", 10))
	})
}

// TestCleanNameCharsScanLimit covers the parameter directly. The two in-package
// callers pass cleanScanLimit; extractPlanTitle passes 0, because it strips a
// prefix from the result and a cut applied first would spend part of the byte
// budget on a prefix that is about to go.
func TestCleanNameCharsScanLimit(t *testing.T) {
	huge := strings.Repeat("a", 100_000)

	t.Run("a positive limit stops the append", func(t *testing.T) {
		assert.Len(t, CleanNameChars(huge, 10), 10)
		assert.Len(t, CleanNameChars(huge, cleanScanLimit), cleanScanLimit)
	})

	t.Run("zero or less scans the whole input", func(t *testing.T) {
		assert.Len(t, CleanNameChars(huge, 0), 100_000)
		assert.Len(t, CleanNameChars(huge, -1), 100_000)
	})

	// The stop bounds the OUTPUT, so it never cuts a result that already fits.
	t.Run("a limit above the result changes nothing", func(t *testing.T) {
		assert.Equal(t, "Ship the parser", CleanNameChars("  Ship \t the   parser  ", 1000))
		assert.Equal(t, "Ship the parser", CleanNameChars("  Ship \t the   parser  ", 0))
	})

	// The break fires AFTER the write, so a multi-byte rune can carry the
	// result up to three bytes past the limit. Both callers depend on the
	// result being AT LEAST the limit rather than at most, so state it.
	t.Run("a multi-byte rune can overshoot the limit by up to three bytes", func(t *testing.T) {
		got := CleanNameChars(strings.Repeat("\u4e00", 100), 10)
		assert.GreaterOrEqual(t, len(got), 10)
		assert.LessOrEqual(t, len(got), 12)
		assert.True(t, utf8.ValidString(got))
	})
}

// TestIsNameWhitespaceAndIsUnreadable covers the two exported predicates that
// every rule in this repository reads, so the sets have one definition.
//
// The two sets OVERLAP on purpose, on exactly the whitespace controls
// (U+0009-U+000D and U+0085), which are Cc and White_Space at the same time.
// The ORDER of the tests is what resolves the overlap, and the two callers
// resolve it differently on purpose: CleanNameChars asks the fold FIRST, so a
// newline becomes one space and "Fix parser\nAdd tests" does not read as
// "Fix parserAdd tests"; StripUnreadable does not fold at all, so it removes
// the newline with the other control characters.
func TestIsNameWhitespaceAndIsUnreadable(t *testing.T) {
	// A whitespace control is in BOTH sets. This is the overlap the order
	// exists for, and each caller's answer for it is pinned below.
	for _, r := range []rune{'\t', '\n', '\v', '\f', '\r', 0x0085} {
		assert.Truef(t, IsNameWhitespace(r), "U+%04X must fold", r)
		assert.Truef(t, IsUnreadable(r), "U+%04X is Cc, so it is unreadable too", r)
		assert.Equalf(t, "a b", CleanName("a"+string(r)+"b"),
			"CleanNameChars asks the fold first, so U+%04X becomes one space", r)
		assert.Equalf(t, "ab", StripUnreadable("a"+string(r)+"b", 0),
			"StripUnreadable does not fold, so U+%04X goes with the other controls", r)
	}

	// A whitespace character that is NOT a control character folds and is
	// never unreadable, so both callers agree about it.
	for _, r := range []rune{' ', 0x00A0, 0x1680, 0x2000, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000} {
		assert.Truef(t, IsNameWhitespace(r), "U+%04X must fold", r)
		assert.Falsef(t, IsUnreadable(r), "U+%04X is visible whitespace, not an unreadable character", r)
	}

	// An unreadable character that is not whitespace is stripped by both.
	for _, r := range []rune{0x0000, 0x001F, 0x007F, 0x009F, 0x00AD, 0x200B, 0x202E, 0xFEFF} {
		assert.Truef(t, IsUnreadable(r), "U+%04X must be unreadable", r)
		assert.Falsef(t, IsNameWhitespace(r), "U+%04X is not whitespace", r)
		assert.Equalf(t, "ab", CleanName("a"+string(r)+"b"), "U+%04X is stripped", r)
		assert.Equalf(t, "ab", StripUnreadable("a"+string(r)+"b", 0), "U+%04X is stripped", r)
	}

	// Visible text, and the three invisible characters the name rule keeps.
	for _, r := range []rune{'a', 'Z', '0', '"', '\\', '$', '%', 0x4E00, 0x200C, 0x200D, 0xFE0F, 0x1F600} {
		assert.Falsef(t, IsNameWhitespace(r), "U+%04X is not whitespace", r)
		assert.Falsef(t, IsUnreadable(r), "U+%04X must survive", r)
	}
}
