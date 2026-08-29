package service

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The OAuth pages state the default palette inline because `default-src
// 'none'` forbids linking the SPA's stylesheet (see the pageCSS comment).
// That makes the Go copy a hand-kept mirror of
// frontend/src/styles/themes/default.ts, and a theme edit that forgets the
// copy leaves the authorization screens a release behind the app with no
// test failing. This test is the pin: every token pageCSS states must equal
// the default palette's value for the same token, in both polarities.
//
// The page renames one token: the page's --danger-subtle is the palette's
// --lm-danger-subtle (the page has no lm- namespace to keep).

// palettePath is the default palette, relative to this package's directory.
const palettePath = "../../../../frontend/src/styles/themes/default.ts"

// tokenValuePattern matches one `'--token': 'value',` line of default.ts.
var tokenValuePattern = regexp.MustCompile(`'(--[a-z0-9-]+)':\s*'([^']+)'`)

// pageTokenPattern matches one `--token: value;` line of pageCSS.
var pageTokenPattern = regexp.MustCompile(`(--[a-z0-9-]+):\s*([^;]+);`)

// paletteVariant extracts the named variant object (`light` or `dark`) from
// default.ts as a token map.
func paletteVariant(t *testing.T, source, variant string) map[string]string {
	t.Helper()
	start := strings.Index(source, "const "+variant+" = {")
	require.GreaterOrEqualf(t, start, 0,
		"default.ts no longer declares a %s palette object", variant)
	end := strings.Index(source[start:], "\n}")
	require.NotEqualf(t, -1, end,
		"default.ts no longer closes its %s palette object", variant)
	out := map[string]string{}
	for _, m := range tokenValuePattern.FindAllStringSubmatch(source[start:start+end], -1) {
		out[m[1]] = m[2]
	}
	return out
}

// pageVariant extracts the light (:root) or dark (@media) custom-property
// block from pageCSS as a token map.
func pageVariant(t *testing.T, css, opener string) map[string]string {
	t.Helper()
	start := strings.Index(css, opener)
	require.GreaterOrEqualf(t, start, 0, "pageCSS no longer declares %q", opener)
	block := css[start:]
	if end := strings.Index(block, "}"); end >= 0 {
		block = block[:end]
	}
	out := map[string]string{}
	for _, m := range pageTokenPattern.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

func TestPageCSSMatchesTheDefaultPalette(t *testing.T) {
	raw, err := os.ReadFile(palettePath)
	if err != nil {
		// FAIL, not skip: the palette is a git-controlled source file, so a
		// read failure means it moved or was renamed -- exactly the drift
		// this pin exists to catch. (The Oat test below keeps its skip:
		// node_modules is genuinely absent until `bun install`.)
		t.Fatalf("default palette not reachable from this checkout: %v", err)
	}
	source := string(raw)

	// The page's name for the palette's --lm-danger-subtle.
	pageNameForToken := map[string]string{
		"--danger-subtle": "--lm-danger-subtle",
	}

	for _, variant := range []struct{ page, palette string }{
		{":root {", "light"},
		{"@media (prefers-color-scheme: dark) {\n  :root {", "dark"},
	} {
		page := pageVariant(t, pageCSS, variant.page)
		palette := paletteVariant(t, source, variant.palette)
		require.NotEmptyf(t, page, "pageCSS %s block states no tokens", variant.palette)
		for name, value := range page {
			paletteName := pageNameForToken[name]
			if paletteName == "" {
				paletteName = name
			}
			want, ok := palette[paletteName]
			if !ok {
				assert.Failf(t, "unknown pageCSS token",
					"pageCSS states %s, which the default %s palette does not define; "+
						"the page may only restate palette tokens", name, variant.palette)
				continue
			}
			assert.Equalf(t, want, value,
				"pageCSS %s: change the copy with the palette (see the pageCSS comment)",
				name)
		}
	}
}

// The page's .tick rules restate Oat's own checkbox piece for piece (see
// the .tick comment in pageCSS), which makes them a second hand-kept copy
// of @knadh/oat's form stylesheet. This test pins the copy the same way the
// palette test pins the palette: the check the tick draws is the byte-exact
// SVG mask Oat's checkbox draws, so an Oat release that reshapes its mark
// fails the suite here instead of leaving every consent page's tick a
// release behind the Preferences dialog's checkboxes.

// oatFormCSSPath is Oat's form stylesheet, relative to this package.
const oatFormCSSPath = "../../../../frontend/node_modules/@knadh/oat/css/form.css"

// oatMaskPattern matches the mask-image URL inside Oat's checked-checkbox
// rule (form.css nests it under `input[type=checkbox]`'s `&:checked::after`).
var oatMaskPattern = regexp.MustCompile(
	`input\[type=checkbox\][^}]*&:checked::after\s*\{[^}]*mask-image:\s*url\("([^"]+)"\)`)

// pageMaskPattern matches the mask-image URL of the page's granted tick.
var pageMaskPattern = regexp.MustCompile(
	`\.granted > \.tick::after\s*\{[^}]*mask-image:\s*url\("([^"]+)"\)`)

func TestPageTickMatchesTheOatCheckbox(t *testing.T) {
	raw, err := os.ReadFile(oatFormCSSPath)
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
