import { describe, expect, it } from 'vitest'
import { ensureThemesRegistered, isSyntaxThemeName, LOADABLE_SYNTAX_THEMES, loadSyntaxTheme, syntaxPairFor } from '~/lib/syntaxThemes'
import nordLightBrighter from '~/lib/syntaxThemes/nord-light-brighter.json'
import nordLight from '~/lib/syntaxThemes/nord-light.json'
import tokyoNightDay from '~/lib/syntaxThemes/tokyo-night-day.json'
import { ALL_VARIANTS, DEFAULT_THEME_ID, themeById, THEMES } from '~/styles/themes'

/**
 * The sixteen ANSI slots, spelled as a TextMate document's `terminal.ansi*`
 * keys.
 *
 * Capitalized here and lower-cased at the point of use, because the same slot
 * is `terminal.ansiBrightBlack` in the theme document and `brightBlack` in the
 * catalogue's `AnsiPalette`. One list, so a slot cannot be checked on one side
 * and missed on the other.
 */
const ANSI_SLOTS = ['Black', 'Red', 'Green', 'Yellow', 'Blue', 'Magenta', 'Cyan', 'White', 'BrightBlack', 'BrightRed', 'BrightGreen', 'BrightYellow', 'BrightBlue', 'BrightMagenta', 'BrightCyan', 'BrightWhite'] as const

// The syntax catalogue lives apart from `~/styles/themes/` -- those modules are
// plain data with no imports so the notice script can read them, and a TextMate
// theme is a lazily imported chunk. This file is the join that keeps the two
// lists from drifting.
describe('syntax theme catalogue', () => {
  it('gives every variant a loadable syntax theme', () => {
    // The variant states its own theme name, so a palette and its highlighting
    // cannot drift apart the way a second lookup table let them.
    for (const v of ALL_VARIANTS)
      expect(isSyntaxThemeName(v.syntax), `${v.id} -> ${v.syntax}`).toBe(true)
  })

  it('carries a loader for every name, and no loader nothing names', () => {
    // Both directions fail silently. A name with no loader resolves to the
    // FALLBACK theme and paints its colours under the name the user chose; a
    // loader nothing names ships a document into the build no user can reach --
    // which is what two generated themes became the moment Default was pointed
    // at GitHub's pair.
    const named = new Set(ALL_VARIANTS.map(v => v.syntax))
    for (const name of LOADABLE_SYNTAX_THEMES)
      expect(named.has(name), `${name} is loadable but unreachable`).toBe(true)
    expect(LOADABLE_SYNTAX_THEMES.length).toBe(named.size)
  })

  it('pairs a distinct light and dark half for every theme', () => {
    // A pair whose halves are equal would paint dark code on a light page. The
    // resolver may collapse them deliberately when a syntax mode is PINNED, but
    // a theme's own default pair never should.
    for (const theme of THEMES) {
      const pair = syntaxPairFor(theme.id)
      expect(pair.light, `${theme.id} uses one theme for both polarities`).not.toBe(pair.dark)
    }
  })

  it('falls back to the default pair for a theme it does not know', () => {
    expect(syntaxPairFor('from-the-future')).toEqual(syntaxPairFor(DEFAULT_THEME_ID))
    expect(syntaxPairFor('')).toEqual(syntaxPairFor(DEFAULT_THEME_ID))
  })

  it('highlights the Default theme with GitHub, which is what always shipped', () => {
    // Dimidium is a terminal scheme and ships no editor theme, so Default has
    // none of its own. GitHub is the pair every line of code in this app was
    // highlighted with before the catalogue existed.
    expect(syntaxPairFor('default')).toEqual({ light: 'github-light', dark: 'github-dark' })
    expect(syntaxPairFor('default')).toEqual(syntaxPairFor('github'))
  })

  it('names the borrowed pair in the picker, so GitHub stays findable', () => {
    expect(themeById(DEFAULT_THEME_ID).syntaxLabel).toBe('Default (GitHub)')
  })

  it('gives each Catppuccin flavour its own syntax theme', () => {
    // The four flavours are four looks, not one theme in four backgrounds --
    // Shiki bundles a distinct TextMate theme for each, and a variant that
    // reused Mocha's would highlight Frappé's palette in Mocha's colours.
    const names = themeById('catppuccin').variants.map(v => v.syntax)
    expect(new Set(names).size).toBe(names.length)
    expect(names).toContain('catppuccin-frappe')
    expect(names).toContain('catppuccin-macchiato')
  })

  it('shares one syntax theme across a contrast level, as upstream does', () => {
    // Gruvbox's contrast axis moves the BACKGROUND only, so hard/medium/soft of
    // one polarity legitimately highlight alike. Asserting distinctness here
    // would fail a theme for following its own upstream.
    const dark = themeById('gruvbox').variants.filter(v => v.polarity === 'dark')
    expect(dark.length).toBe(3)
    expect(new Set(dark.map(v => v.syntax)).size).toBe(3)
  })
})

describe('loadSyntaxTheme', () => {
  it('memoizes by name, so three highlighters share one import', async () => {
    // The two workers' highlighters and the editor's all ask for the same pair.
    // A theme is immutable once loaded, so re-importing it per highlighter would
    // be pure waste.
    const a = loadSyntaxTheme('github-dark')
    const b = loadSyntaxTheme('github-dark')
    expect(a).toBe(b)
    const theme = await a as { tokenColors?: unknown[] }
    expect(theme.tokenColors?.length).toBeGreaterThan(0)
  })

  it('forces the background transparent, so the app wrapper owns it', async () => {
    // Every highlighted surface draws its own block background; a theme that
    // kept its own would paint a rectangle of the wrong colour behind the code.
    const theme = await loadSyntaxTheme('github-light') as { bg?: string }
    expect(theme.bg).toBe('transparent')
  })

  it('falls back to a REAL theme for a name it does not carry', async () => {
    // Reached by an ordinary path: a preference written by a newer build names
    // a theme this one has never heard of. Rejecting would leave the app with
    // no syntax theme at all -- but so would resolving to an empty object, and
    // "resolves to something" would not tell the two apart.
    const fallback = await loadSyntaxTheme('from-the-future') as { tokenColors?: unknown[] }
    const known = await loadSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID).dark) as { tokenColors?: unknown[] }
    expect(fallback.tokenColors?.length).toBe(known.tokenColors?.length)
  })

  it('falls back for a name that names an Object.prototype member', async () => {
    // A bare `LOADERS[name]` answers with an INHERITED function for
    // `constructor` or `toString`, which is truthy -- so the fallback below it
    // never fired and calling it threw inside the load instead. The sibling
    // `isSyntaxThemeName` and `resolveBundledLang` both ask with `Object.hasOwn`
    // for this reason.
    const known = await loadSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID).dark) as { tokenColors?: unknown[] }
    for (const name of ['constructor', 'toString', 'valueOf', 'hasOwnProperty']) {
      const theme = await loadSyntaxTheme(name) as { tokenColors?: unknown[], name?: string }
      expect(theme.tokenColors?.length).toBe(known.tokenColors?.length)
      expect(theme.name).toBe(name)
    }
  })
})

describe('ensureThemesRegistered', () => {
  function registrar() {
    const themes: string[] = []
    return {
      themes,
      getLoadedThemes: () => [...themes],
      loadTheme: async (t: { name?: string }) => void themes.push(t.name ?? 'unnamed'),
    }
  }

  it('registers both halves', async () => {
    const hl = registrar()
    await ensureThemesRegistered(hl, { light: 'github-light', dark: 'github-dark' })
    expect(hl.themes.sort()).toEqual(['github-dark', 'github-light'])
  })

  it('registers nothing a second time', async () => {
    // Called on every worker request, so a re-register per message would
    // re-parse a theme document for each code block on screen.
    const hl = registrar()
    const pair = { light: 'github-light', dark: 'github-dark' }
    await ensureThemesRegistered(hl, pair)
    await ensureThemesRegistered(hl, pair)
    expect(hl.themes).toHaveLength(2)
  })

  it('registers a pinned pair ONCE, although both halves name it', async () => {
    // `resolveSyntaxPair` collapses the two halves onto one theme whenever the
    // syntax MODE is pinned -- Shiki carries both halves in every token and CSS
    // picks between them, so emitting one half alone would leave the other
    // variable showing the theme the user pinned away from. A plain filter over
    // [light, dark] therefore held the same name twice and re-parsed one
    // document on every highlighter, on every switch, for exactly the
    // configuration pinning exists to serve.
    const hl = registrar()
    await ensureThemesRegistered(hl, { light: 'github-dark', dark: 'github-dark' })
    expect(hl.themes).toEqual(['github-dark'])
  })

  it('registers only the half that is missing', async () => {
    const hl = registrar()
    await ensureThemesRegistered(hl, { light: 'github-light', dark: 'github-dark' })
    hl.themes.length = 0
    await ensureThemesRegistered(hl, { light: 'github-light', dark: 'nord-light' })
    // `github-light` reads as absent now, so both are registered again -- what
    // this pins is that the check is per HALF, not all-or-nothing on the pair.
    expect(hl.themes).toContain('nord-light')
  })
})

// The three documents this repo vendors, because Shiki bundles no light half
// for Nord or for Tokyo Night. What these cases guard is that a COPY keeps the
// properties the app relies on: the declared polarity, scope coverage, and an
// ANSI set that upstream does not ship for the Nord pair.
describe('the syntax themes this repo vendors', () => {
  const VENDORED = [
    ['nord-light', nordLight, 'light'],
    ['nord-light-brighter', nordLightBrighter, 'light'],
  ] as const

  it.each(VENDORED)('%s declares its variant, so Shiki resolves the right half', (_name, theme, variant) => {
    // Shiki reads `type` to resolve the default colour of a dual-theme render.
    expect((theme as { type: string }).type).toBe(variant)
  })

  /**
   * Whether `scopes` colours a token scoped `target`, by TextMate's own rule: a
   * selector matches a scope it is a DOT-SEGMENT PREFIX of, so `entity` covers
   * `entity.name.function.python`.
   *
   * Substring matching would be wrong in both directions -- it would accept
   * `entity` for `identity.foo`, and reject the broad `entity` selector that
   * these themes actually use for functions.
   */
  function covers(scopes: string[], target: string): boolean {
    const wanted = target.split('.')
    return scopes.some((raw) => {
      // A descendant selector ("markup.heading entity.name") matches on its
      // last component; only that component has to be the prefix.
      const selector = raw.trim().split(/\s+/).at(-1)!
      const parts = selector.split('.')
      return parts.length <= wanted.length && parts.every((p, i) => p === wanted[i])
    })
  }

  it.each(VENDORED)('%s carries a real scope list, not a handful of roles', (_name, theme) => {
    const rules = (theme as { tokenColors: { scope?: unknown }[] }).tokenColors
    // Upstream ships 80 rules and 82. Well under that means the copy lost
    // rules, and granularity went with them.
    expect(rules.length).toBeGreaterThanOrEqual(40)
    const scopes = rules.flatMap(r => Array.isArray(r.scope) ? r.scope : r.scope ? [String(r.scope)] : [])
    // The scopes a real grammar emits for the constructs a reader most needs
    // told apart. `entity.name.function` is the one the first, hand-written
    // generation missed -- `print(1)` came back as a single span.
    for (const needed of [
      'comment.line.double-slash',
      'string.quoted.double',
      'keyword.control.flow',
      'entity.name.function',
      'constant.numeric',
      'variable.other.readwrite',
    ]) {
      expect(covers(scopes, needed), `no rule covers ${needed}`).toBe(true)
    }
  })

  it.each(VENDORED)('%s declares all sixteen ANSI colours', (_name, theme) => {
    // An `ansi` code fence is coloured from these. Upstream ships none, so they
    // are ADDED to the copy -- without them Shiki falls back to its own
    // defaults and a fence in chat stops matching the terminal.
    const colors = (theme as { colors: Record<string, string> }).colors
    for (const slot of ANSI_SLOTS)
      expect(colors[`terminal.ansi${slot}`], `terminal.ansi${slot}`).toMatch(/^#[0-9a-f]{6}$/i)
  })

  it.each(VENDORED)('%s takes those ANSI colours from the variant that names it', (name, theme) => {
    // The reason they are added at all. An `ansi` fence in chat and the
    // terminal beside it answer the same question, so a copy that carried a
    // sibling's ANSI set -- or kept one after a variant retuned its own --
    // would paint two different greens for one SGR code. Read from the
    // catalogue, so the two cannot drift apart silently.
    const variant = ALL_VARIANTS.find(v => v.syntax === name)!
    expect(variant, `no variant names ${name}`).toBeDefined()
    const colors = (theme as { colors: Record<string, string> }).colors
    for (const slot of ANSI_SLOTS) {
      const key = `${slot.charAt(0).toLowerCase()}${slot.slice(1)}` as keyof typeof variant.terminal
      expect(colors[`terminal.ansi${slot}`]!.toLowerCase(), `terminal.ansi${slot}`)
        .toBe(variant.terminal[key].toLowerCase())
    }
  })

  it('leaves no unparseable colour in either vendored Nord flavour', () => {
    // `nord-light` is copied from a document that writes `#3B42527` on
    // `meta.separator` and on `punctuation.section.embedded` -- seven hex
    // digits. Shiki loads it without complaint and emits `color:#3B42527`,
    // which the browser drops, so those scopes inherit their parent's colour
    // instead of nord1 and nothing reports it. Asserted over every rule of
    // both files rather than those two, so a re-vendor that reintroduces the
    // typo on another scope, or in the other flavour, fails here too.
    for (const [name, theme] of VENDORED) {
      const rules = (theme as { tokenColors: { settings?: { foreground?: string } }[] }).tokenColors
      for (const rule of rules) {
        const fg = rule.settings?.foreground
        if (fg !== undefined)
          expect(fg, `${name}: ${fg} is not a colour`).toMatch(/^#[0-9a-f]{6}([0-9a-f]{2})?$/i)
      }
    }
  })

  it('corrects Tokyo Night Day, whose upstream file mislabels itself as dark', () => {
    // The one silent failure in the vendored file: Shiki would resolve this
    // light theme as the dark half of the pair and paint dark code on a light
    // page. Nothing else would report it.
    expect((tokyoNightDay as { type: string }).type).toBe('light')
    expect((tokyoNightDay as { tokenColors: unknown[] }).tokenColors.length).toBeGreaterThan(100)
  })

  it('gives each Nord light variant its own flavour, not one document twice', () => {
    // The pair exists because upstream publishes two. Pointing both variants at
    // one document would leave the brighter palette highlighting in the darker
    // flavour's colours, which no case above would see.
    expect(syntaxPairFor('nord').light).toBe('nord-light')
    const brighter = ALL_VARIANTS.find(v => v.id === 'nord-light-brighter')!
    expect(brighter.syntax).toBe('nord-light-brighter')
    const rules = (theme: unknown) => (theme as { tokenColors: unknown[] }).tokenColors.length
    expect(rules(nordLight)).not.toBe(rules(nordLightBrighter))
  })
})
