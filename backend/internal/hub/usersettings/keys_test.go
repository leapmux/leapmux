package usersettings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// SanitizeName STRIPS what it does not want rather than failing on it, so a
// validator that only reports its error let the raw name through — and the raw
// name is what gets stored and later emitted into a CSS font-family value.
func TestFontFamilyRefusesANameThatIsNotAlreadySanitized(t *testing.T) {
	for _, name := range []string{
		"Fira\tCode",
		"Fira\x00Code",
		"Fira\u200bCode",
		"Fira\ufeffCode",
		"Fira  Code",
		"  Fira Code",
		"Fira Code  ",
	} {
		err := KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{name}})
		require.Error(t, err, "font name %q must be refused, not silently stored raw", name)
		assert.Contains(t, err.Error(), "invalid font name")
	}
}

// The name rule relaxed, and this key relaxed with it. The CSS stays safe,
// because buildFontFamily escapes a quote and a backslash at the emitter — the
// one guard that also covers the hand-edited localStorage document, which never
// reaches this validator.
func TestFontFamilyAcceptsTheCharactersTheNameRuleUsedToStrip(t *testing.T) {
	for _, name := range []string{
		`Fira Code"; color:red`,
		`Fira\Code`,
		`Fira$Code`,
		`Fira%Code`,
	} {
		assert.NoErrorf(t, KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{name}}),
			"font name %q must be accepted; the emitter escape is what keeps the CSS safe", name)
	}
}

func TestFontFamilyAcceptsAlreadySanitizedNames(t *testing.T) {
	require.NoError(t, KeyUIFonts.Validate(FontFamilyValue{
		Enabled: true,
		Fonts:   []string{"Fira Code", "JetBrains Mono", "Menlo"},
	}))
	require.NoError(t, KeyMonoFonts.Validate(FontFamilyValue{}), "the empty stack is the default")
}

func TestFontFamilyStillRefusesEmptyAndOverlongNames(t *testing.T) {
	require.Error(t, KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{"   "}}))
	require.Error(t, KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{strings.Repeat("A", 129)}}))
}

// TestScalarEnumKeysRefuseAnUnlistedValue is the tripwire for the whole
// class, not for one key.
//
// A declared enum used to be a rendering hint only: `theme` ran a slug
// check that accepted any lowercase word, and `diff_view`/`turn_end_sound`
// had no validator at all. The hub stored whatever arrived, the client's
// own parse then refused it and fell back to the default, and the dialog
// showed the default beside a "Customized" badge — a state only Reset
// could leave. Every scalar enum key must refuse a value it does not
// advertise, so a key added without a validator fails here.
func TestScalarEnumKeysRefuseAnUnlistedValue(t *testing.T) {
	checked := 0
	for _, desc := range descriptors() {
		fields := desc.UI().Fields
		if len(fields) != 1 || fields[0].Name != "" || fields[0].Kind != settings.FieldEnum {
			continue
		}
		checked++
		err := desc.Validate("definitely-not-an-advertised-value")
		require.Errorf(t, err, "%s advertises an enum but stores an unlisted value", desc.Name())
		for _, ev := range fields[0].EnumValues {
			assert.NoErrorf(t, desc.Validate(ev.Value),
				"%s advertises %q but its validator refuses it", desc.Name(), ev.Value)
		}
	}
	require.NotZero(t, checked, "no scalar enum key was checked; the walk proved nothing")
}

// TestThemeStoresOnlyTheSanitizedSpelling pins the specific regression:
// SanitizeSlug lowercases BEFORE it validates, so a validator that keeps
// only its error accepted "Dark" and stored it verbatim.
func TestThemeStoresOnlyTheSanitizedSpelling(t *testing.T) {
	for _, raw := range []string{"Dark", " dark", "dark "} {
		assert.Errorf(t, KeyTheme.Validate(raw),
			"theme %q is not the stored spelling of any advertised value; it must be refused", raw)
	}
	require.NoError(t, KeyTheme.Validate("dark"))
}

// The refusal reports the CLEANED FORM rather than a list of causes.
//
// SanitizeName rewrites as well as strips, so a list of causes cannot stay
// correct: a no-break space folds to a plain space, and it is neither a
// control character, nor an invisible format character, nor a repeated space.
// The message enumerated exactly those three and named none of this input.
func TestFontFamilyRefusalReportsTheCleanedForm(t *testing.T) {
	err := KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{"Fira\u00a0Code"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be its cleaned form")
	// %q ESCAPES the unprintable half, so the user reads `"Fira\u00a0Code"`
	// against `"Fira Code"` and can see a difference the screen hides. Go's
	// IsPrint excludes every Zs but U+0020, which is what makes the refused
	// form legible here.
	assert.Contains(t, err.Error(), `"Fira\u00a0Code"`)
	assert.Contains(t, err.Error(), `"Fira Code"`)
}

// Every non-ASCII whitespace character folds, and the old enumeration named
// none of them. These are the inputs a user actually pastes: an NBSP from a
// web page, an ideographic space from a Japanese IME.
func TestFontFamilyRefusesEveryFoldedWhitespace(t *testing.T) {
	for _, name := range []string{
		"Fira\u00a0Code", "Fira\u3000Code", "Fira Code",
		"Fira\u202fCode", "Fira\u205fCode", "Fira\u1680Code",
	} {
		err := KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{name}})
		require.Errorf(t, err, "font name %q folds, so it must be refused", name)
		assert.Contains(t, err.Error(), "invalid font name")
	}
}

// The per-name BYTE cap runs before either %q verb.
//
// The list cap bounds how MANY names an account holds and says nothing about
// how large one is, so one 4 MiB name -- the hub's whole request limit --
// reached `fmt.Errorf("invalid font name %q", name)` and became a multi-megabyte
// error string in an uncapped Connect response and in a log line. %q expands
// an unprintable byte roughly fourfold on the way.
func TestFontFamilyCapsOneNameBeforeItEchoesIt(t *testing.T) {
	t.Run("reports the length rather than echoing the name", func(t *testing.T) {
		huge := strings.Repeat("\x7f", 1_000_000)
		err := KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{huge}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be at most 128 bytes")
		assert.Contains(t, err.Error(), "at index 0", "the message must locate the entry")
		assert.Less(t, len(err.Error()), 1_000,
			"the error must not carry the name it refused; %%q would have expanded it fourfold")
	})

	t.Run("names the offending index", func(t *testing.T) {
		err := KeyUIFonts.Validate(FontFamilyValue{
			Enabled: true,
			Fonts:   []string{"Fira Code", strings.Repeat("a", 129)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at index 1")
	})

	t.Run("the limit is inclusive", func(t *testing.T) {
		require.NoError(t, KeyUIFonts.Validate(FontFamilyValue{
			Enabled: true, Fonts: []string{strings.Repeat("a", 128)},
		}))
		require.Error(t, KeyUIFonts.Validate(FontFamilyValue{
			Enabled: true, Fonts: []string{strings.Repeat("a", 129)},
		}))
	})

	// The cap counts BYTES, so a wide script reaches it with fewer characters.
	t.Run("counts UTF-8 bytes", func(t *testing.T) {
		require.NoError(t, KeyUIFonts.Validate(FontFamilyValue{
			Enabled: true, Fonts: []string{strings.Repeat("一", 42)},
		}), "42 CJK characters is 126 bytes")
		require.Error(t, KeyUIFonts.Validate(FontFamilyValue{
			Enabled: true, Fonts: []string{strings.Repeat("一", 43)},
		}), "43 CJK characters is 129 bytes")
	})
}
