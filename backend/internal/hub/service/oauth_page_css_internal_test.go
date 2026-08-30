package service

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The page's .tick rules restate Oat's own checkbox piece for piece (see
// the .tick comment in pageCSS), which makes them a second hand-kept copy
// of @knadh/oat's form stylesheet. This test pins the copy the same way the
// generated palette pins the palette: the check the tick draws is the
// byte-exact SVG mask Oat's checkbox draws, so an Oat release that reshapes
// its mark fails the suite here instead of leaving every consent page's tick
// a release behind the Preferences dialog's checkboxes.

// oatFormCssPath is Oat's form stylesheet, relative to this package.
const oatFormCssPath = "../../../../frontend/node_modules/@knadh/oat/css/form.css"

// oatMaskPattern matches the mask-image URL inside Oat's checked-checkbox
// rule (form.css nests it under `input[type=checkbox]`'s `&:checked::after`).
var oatMaskPattern = regexp.MustCompile(
	`input\[type=checkbox\][^}]*&:checked::after\s*\{[^}]*mask-image:\s*url\("([^"]+)"\)`)

// pageMaskPattern matches the mask-image URL of the page's granted tick.
var pageMaskPattern = regexp.MustCompile(
	`\.granted > \.tick::after\s*\{[^}]*mask-image:\s*url\("([^"]+)"\)`)

func TestPageTickMatchesTheOatCheckbox(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(oatFormCssPath)
	if err != nil {
		t.Skipf("Oat form stylesheet not reachable from this checkout: %v", err)
	}
	oat := oatMaskPattern.FindStringSubmatch(string(raw))
	require.Lenf(t, oat, 2,
		"Oat's checkbox rule no longer states a checked mask-image URL; "+
			"the .tick copy in pageCSS must follow whatever shape it took")
	page := pageMaskPattern.FindStringSubmatch(pageCSS)
	require.Lenf(t, page, 2,
		"pageCSS no longer draws the granted tick through a mask-image URL")
	assert.Equalf(t, oat[1], page[1],
		"the consent page's tick must draw the same check as Oat's own checkbox "+
			"(see the .tick comment in pageCSS); copy the new mask or restyle both")
}

// The hand-written rules below the generated half may only read palette
// tokens the generated half actually defines: contracts/theme-default.json's
// oauthPage.tokens list is the page's whole palette, and a var() reference to
// a token outside it silently renders unset (an invisible or undifferentiated
// element on every authorization screen) with nothing red anywhere.
func TestPageCssReadsOnlyGeneratedPaletteTokens(t *testing.T) {
	t.Parallel()
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`--([a-z0-9-]+):\s*[^;]+;`).FindAllStringSubmatch(contracts.OAuthPagePaletteCSS, -1) {
		defined[m[1]] = true
	}
	require.NotEmpty(t, defined, "the generated palette half defines no tokens")
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`var\(--([a-z0-9-]+)\)`).FindAllStringSubmatch(pageCSS, -1) {
		used[m[1]] = true
	}
	require.NotEmpty(t, used, "the hand-written rules read no palette tokens")
	for token := range used {
		assert.Truef(t, defined[token],
			"pageCSS reads var(--%s), which the generated palette half does not define; "+
				"add the token to contracts/theme-default.json oauthPage.tokens", token)
	}
	// The generated half must be spliced in, not merely present in the binary.
	assert.True(t, strings.Contains(pageCSS, "color-scheme: light dark"),
		"pageCSS lost the generated palette splice")
}
