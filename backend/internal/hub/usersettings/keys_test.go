package usersettings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// SanitizeName STRIPS a quote rather than failing on it, so a validator
// that only reports its error let the raw name through — and the raw name
// is what gets stored and later emitted into a CSS font-family value.
func TestFontFamilyRefusesANameThatIsNotAlreadySanitized(t *testing.T) {
	for _, name := range []string{
		`Fira Code"; color:red`,
		`Fira\Code`,
		`Fira$Code`,
		`Fira%Code`,
		"Fira\tCode",
		"  Fira Code",
		"Fira Code  ",
	} {
		err := KeyUIFonts.Validate(FontFamilyValue{Enabled: true, Fonts: []string{name}})
		require.Error(t, err, "font name %q must be refused, not silently stored raw", name)
		assert.Contains(t, err.Error(), "invalid font name")
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
