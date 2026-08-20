import type { ThemePalette } from '~/styles/themes'
import { describe, expect, it } from 'vitest'

import { DIFF_TINT } from '~/styles/diffTint'
import { ALL_VARIANTS, DEFAULT_THEME_ID, isThemeId, paletteColorToHex, resolveVariant, themeById, themeLabel, THEMES, variantsFor } from '~/styles/themes'
import { defaultTheme } from '~/styles/themes/default'
import { oneTheme } from '~/styles/themes/one'
import { colorDistance, contrast, luminance, mixOver, parseColor } from '~/test-support/color'

// These modules have two consumers that cannot check each other: global.css.ts
// spreads them into the app's theme selectors, and scripts/generate-notice.mjs
// inlines the default palette into the standalone NOTICE.html page. A malformed
// entry surfaces as a colour that silently falls back to Oat's default on one
// surface or the other -- and with eleven themes, on one theme and not the rest.

const VARIANTS = ['light', 'dark'] as const

/**
 * Every variant of every theme, so a case can state which one it is asserting.
 *
 * `variant` is the polarity and `id` the variant's own id, so a failure names
 * the exact palette -- `catppuccin-frappe/dark`, not `catppuccin/dark`, which
 * would be ambiguous now that a theme can carry three dark looks.
 */
const ALL: { id: string, variant: 'light' | 'dark', palette: ThemePalette, terminal: Record<string, string> }[]
  = ALL_VARIANTS.map(v => ({
    id: v.id,
    variant: v.polarity,
    palette: v.palette,
    terminal: v.terminal as unknown as Record<string, string>,
  }))

/** Hue in degrees, 0-360. Undefined for a grey, which has no hue to compare. */
function hue(value: string): number | undefined {
  const parsed = parseColor(value)
  if (!parsed)
    throw new Error(`cannot read the hue of ${value}`)
  const [r, g, b] = parsed.map(c => c / 255) as [number, number, number]
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  if (max === min)
    return undefined
  const d = max - min
  const h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4
  return (h * 60 + 360) % 360
}

/** The shorter way round the colour wheel between two hues, in degrees. */
function hueGap(a: string, b: string): number {
  const [x, y] = [hue(a), hue(b)]
  if (x === undefined || y === undefined)
    return 0
  const raw = Math.abs(x - y)
  return Math.min(raw, 360 - raw)
}

describe('theme catalogue', () => {
  it('lists the default theme first and gives every theme a unique kebab-case id', () => {
    expect(THEMES.length).toBeGreaterThan(1)
    expect(THEMES[0]!.id).toBe(DEFAULT_THEME_ID)
    for (const theme of THEMES) {
      expect(theme.id, `${theme.id} is not a kebab-case slug`).toMatch(/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/)
      expect(theme.label.trim(), `${theme.id} has no label`).not.toBe('')
    }
    expect(new Set(THEMES.map(t => t.id)).size, 'duplicate theme id').toBe(THEMES.length)
    expect(new Set(THEMES.map(t => t.label)).size, 'duplicate theme label').toBe(THEMES.length)
  })

  it('names a borrowed terminal palette after the project it came from', () => {
    // Default's sixteen ANSI colours are Dimidium's, and a picker that lists
    // them as "Default" leaves a user who wants Dimidium unable to find the
    // palette they are already looking at.
    const dflt = themeById(DEFAULT_THEME_ID)
    expect(dflt.terminalLabel).toBe('Default (Dimidium)')
    expect(dflt.terminalLabel).toContain(dflt.terminalCredit!.project)
    expect(dflt.terminalLabel).toContain(dflt.label)
  })

  it('leaves every other theme naming its own terminal palette', () => {
    // The other ten supply their own ANSI set, so the theme's name IS the
    // palette's name and an override would only be a second thing to keep true.
    for (const theme of THEMES) {
      if (theme.id === DEFAULT_THEME_ID)
        continue
      expect(theme.terminalLabel, `${theme.id} overrides its terminal name`).toBeUndefined()
      expect(theme.syntaxLabel, `${theme.id} overrides its syntax name`).toBeUndefined()
    }
  })

  it('keeps the names on each surface distinct, so no two options read alike', () => {
    for (const surface of ['ui', 'terminal', 'syntax'] as const) {
      const labels = THEMES.map(t => themeLabel(t, surface))
      expect(new Set(labels).size, `duplicate ${surface} label`).toBe(THEMES.length)
      for (const label of labels)
        expect(label.trim(), `empty ${surface} label`).not.toBe('')
    }
  })

  it('reads the theme name itself on a surface it does not override', () => {
    const nord = themeById('nord')
    expect(themeLabel(nord, 'ui')).toBe(nord.label)
    expect(themeLabel(nord, 'terminal')).toBe(nord.label)
    expect(themeLabel(nord, 'syntax')).toBe(nord.label)
  })

  it('gives every theme at least one variant of each polarity', () => {
    // A theme with no light variant could not answer a light `data-theme`, and
    // one with no dark variant could not answer a dark OS. `resolveVariant`
    // trusts both, and its fallback past `defaults` would become reachable.
    for (const theme of THEMES) {
      expect(variantsFor(theme, 'light').length, `${theme.id} has no light variant`).toBeGreaterThan(0)
      expect(variantsFor(theme, 'dark').length, `${theme.id} has no dark variant`).toBeGreaterThan(0)
    }
  })

  it('points each default at a variant of its own theme and polarity', () => {
    // `defaults` is the one thing `resolveVariant` reads without checking.
    for (const theme of THEMES) {
      for (const polarity of VARIANTS) {
        const found = theme.variants.find(v => v.id === theme.defaults[polarity])
        expect(found, `${theme.id}: defaults.${polarity} names no variant of this theme`).toBeDefined()
        expect(found!.polarity, `${theme.id}: defaults.${polarity} is the wrong polarity`).toBe(polarity)
      }
    }
  })

  it('gives every variant a unique kebab-case id prefixed by its theme', () => {
    const seen = new Set<string>()
    for (const theme of THEMES) {
      for (const v of theme.variants) {
        expect(v.id, `${v.id} is not a kebab-case slug`).toMatch(/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/)
        expect(v.id.startsWith(`${theme.id}-`), `${v.id} is not prefixed by ${theme.id}`).toBe(true)
        expect(seen.has(v.id), `duplicate variant id ${v.id}`).toBe(false)
        seen.add(v.id)
        expect(v.label.trim(), `${v.id} has no label`).not.toBe('')
        expect(v.syntax.trim(), `${v.id} names no syntax theme`).not.toBe('')
      }
    }
    expect(seen.size).toBe(ALL_VARIANTS.length)
  })

  it('keeps every theme defaulting to the palette it shipped', () => {
    // THE GOLDEN LIST. Adding a variant must not move what an account that
    // never opens the picker sees, and nothing else in this suite would notice
    // a re-default -- every structural and contrast case passes either way.
    expect(Object.fromEntries(THEMES.map(t => [t.id, t.defaults]))).toEqual({
      'default': { light: 'default-light', dark: 'default-dark' },
      'ayu': { light: 'ayu-light', dark: 'ayu-dark' },
      'catppuccin': { light: 'catppuccin-latte', dark: 'catppuccin-mocha' },
      'everforest': { light: 'everforest-light', dark: 'everforest-dark' },
      'github': { light: 'github-light', dark: 'github-dark' },
      'gruvbox': { light: 'gruvbox-light-medium', dark: 'gruvbox-dark-medium' },
      'nord': { light: 'nord-light', dark: 'nord-dark' },
      'one': { light: 'one-light', dark: 'one-dark' },
      'rose-pine': { light: 'rose-pine-dawn', dark: 'rose-pine-main' },
      'solarized': { light: 'solarized-light', dark: 'solarized-dark' },
      'tokyo-night': { light: 'tokyo-night-day', dark: 'tokyo-night-night' },
    })
  })

  it('names its variant axis wherever a theme offers a choice', () => {
    // The label becomes the variant menu's accessible name, so two adjacent
    // menus in one row do not share one. A theme with a single variant per
    // polarity renders no menu and needs no label.
    for (const theme of THEMES) {
      const choosable = VARIANTS.some(p => variantsFor(theme, p).length > 1)
      expect(theme.variantLabel !== undefined, `${theme.id} variantLabel`).toBe(choosable)
    }
  })

  it('credits every adapted theme and only those', () => {
    // The default theme is ours, so it carries no credit. Every other theme is
    // someone else's palette, and its credit is what NOTICE and the theme file
    // header are checked against.
    for (const theme of THEMES) {
      if (theme.id === DEFAULT_THEME_ID) {
        expect(theme.credit, 'the default theme must not claim a third-party credit').toBeUndefined()
        continue
      }
      expect(theme.credit, `${theme.id} has no credit`).toBeDefined()
      expect(theme.credit!.url, `${theme.id} credit url`).toMatch(/^https:\/\//)
      expect(theme.credit!.project.trim(), `${theme.id} credit project`).not.toBe('')
      expect(['MIT', 'Apache-2.0'], `${theme.id} is not permissively licensed`).toContain(theme.credit!.license)
    }
  })

  it('declares every token as a non-empty CSS custom property', () => {
    for (const { id, variant, palette } of ALL) {
      for (const [token, value] of Object.entries(palette)) {
        expect(token, `${id}/${variant}: ${token} is not a custom property`).toMatch(/^--[a-z0-9-]+$/)
        expect(value.trim(), `${id}/${variant}: ${token} has no value`).not.toBe('')
      }
    }
  })

  it('gives every theme exactly the token set the default theme declares', () => {
    // A theme that omits a token does not fall back to another LeapMux theme --
    // it falls back to Oat's own value for that property, which is a hole that
    // only appears after a theme switch. An EXTRA token is just as wrong: no
    // selector would ever read it.
    for (const variant of VARIANTS) {
      const expected = Object.keys(resolveVariant(defaultTheme, undefined, variant).palette).sort()
      for (const v of ALL_VARIANTS.filter(x => x.polarity === variant)) {
        const actual = Object.keys(v.palette).sort()
        expect(actual, `${v.id} token set differs from the default theme`).toEqual(expected)
      }
    }
  })

  it('defines no token that only the light variant has', () => {
    // A light-only token leaves the dark variant silently inheriting Oat's
    // value, which is how a palette gets a hole nobody notices.
    //
    // The reverse is allowed and used: --lm-opencode-inner/outer are dark-only
    // because AgentProviderIcon.tsx reads them as `var(--token, <light value>)`,
    // so the light value lives at the point of use rather than here.
    for (const theme of THEMES) {
      const light = resolveVariant(theme, undefined, 'light').palette
      const dark = resolveVariant(theme, undefined, 'dark').palette
      const lightOnly = Object.keys(light).filter(t => !(t in dark))
      expect(lightOnly, `${theme.id}: defined for light but not dark:\n  ${lightOnly.join('\n  ')}`).toHaveLength(0)
    }
  })

  it('carries the tokens both consumers rely on', () => {
    // Not an exhaustive list -- just the ones whose absence would be visible
    // immediately on either surface.
    for (const { id, variant, palette } of ALL) {
      for (const token of ['--background', '--foreground', '--primary', '--accent', '--border']) {
        expect(palette, `${id}/${variant} is missing ${token}`).toHaveProperty([token])
      }
    }
  })

  it('states every colour token as a parseable colour', () => {
    // The three --scrollbar-* tokens are deliberately not flat colours: two are
    // relative-colour expressions over another token, and one is `transparent`.
    // Everything else must parse, because the contrast cases below measure it.
    //
    // --lm-bg-translucent is NOT exempt, although it is rgba(). `parseColor`
    // reads that form, and the case that tints it from its OWN background
    // asserts on it. The comment used to name it here beside the scrollbar
    // tokens, so a reader adding a fifth non-flat token followed the list and
    // exempted one the contrast cases still measure.
    const derived = new Set(['--scrollbar-thumb', '--scrollbar-thumb-hover', '--scrollbar-track'])
    for (const { id, variant, palette } of ALL) {
      for (const [token, value] of Object.entries(palette)) {
        if (derived.has(token))
          continue
        expect(parseColor(value), `${id}/${variant}: ${token} = "${value}" does not parse`).toBeDefined()
      }
    }
  })

  it('paints every variant at the lightness its polarity claims', () => {
    // Catches a copy-paste that leaves two variants identical, and a variant
    // whose declared polarity disagrees with the background it actually paints
    // -- which would put a light palette under `data-theme="dark"`.
    for (const v of ALL_VARIANTS) {
      const l = luminance(parseColor(v.palette['--background']!)!)
      if (v.polarity === 'light')
        expect(l, `${v.id}: declared light, background is not`).toBeGreaterThan(0.5)
      else
        expect(l, `${v.id}: declared dark, background is not`).toBeLessThan(0.2)
    }
  })

  // The reason this file exists. An adapted palette can satisfy every structural
  // case above and still be unreadable, and no other test in the suite looks at
  // what a colour actually is.
  it.each(ALL)('keeps $id/$variant text readable on every surface it is painted on', ({ palette }) => {
    const pairs: [string, string, number][] = [
      ['--foreground', '--background', 4.5],
      ['--foreground', '--card', 4.5],
      ['--foreground', '--secondary', 4.5],
      ['--foreground', '--accent', 4.5],
      ['--card-foreground', '--card', 4.5],
      ['--secondary-foreground', '--secondary', 4.5],
      ['--accent-foreground', '--accent', 4.5],
      // A label on a FILLED semantic surface, at WCAG AA's 3:1 floor for user
      // interface components rather than the 4.5:1 floor for body text. The
      // looser floor is not a concession to the adapted themes -- it is what
      // Default already ships: white on its light --success is 3.09:1 and on
      // its dark --danger 3.49:1. Raising this to 4.5 would fail the palette
      // that predates every theme in this directory. It still catches the real
      // failure it exists for: a white label on a pastel yellow fill.
      ['--primary-foreground', '--primary', 3],
      ['--danger-foreground', '--danger', 3],
      ['--success-foreground', '--success', 3],
      ['--warning-foreground', '--warning', 3],
      // Dimmed text. --muted-foreground is the comment colour, at the 3:1
      // non-text floor; --faint-foreground is the line-number colour, which
      // recedes further still. Default sits at 2.68:1 (light) and 3.16:1
      // (dark), so 2:1 is the floor below which it stops being legible at all.
      ['--muted-foreground', '--background', 3],
      ['--faint-foreground', '--background', 2],
    ]
    for (const [fg, bg, floor] of pairs) {
      const ratio = contrast(palette[fg]!, palette[bg]!)
      expect(ratio, `${fg} on ${bg} is ${ratio.toFixed(2)}:1, below ${floor}:1`).toBeGreaterThanOrEqual(floor)
    }
  })

  it.each(ALL)('draws $id/$variant borders outside the surface ramp, not inside it', ({ id, palette }) => {
    // A border is drawn on the page, on a card, and on a secondary surface --
    // 102 rules across 42 files read `var(--border)`. So it cannot be measured
    // against --background alone: it has to clear the OUTERMOST surface, or an
    // edge that reads on the page disappears on a panel.
    //
    // Twelve variants failed that, and the old 1.1:1 floor against --background
    // could not see it. Solarized set --border to exactly its --card, so every
    // panel edge on a card was 1.000:1 -- invisible. Gruvbox, Ayu, Catppuccin
    // and Tokyo Night put it INSIDE the ramp, nearer the background than
    // --secondary, which draws the edge on the wrong side of its own surface.
    //
    // The margin is the geometry the default theme already had, and which the
    // adapted palettes were measured against: bg -> card -> secondary ->
    // border, each step further out, with the border clearing the last surface
    // by 0.157 (light) and 0.214 (dark).
    const bg = palette['--background']!
    const outermost = Math.max(contrast(palette['--card']!, bg), contrast(palette['--secondary']!, bg))
    const border = contrast(palette['--border']!, bg)
    expect(border, `${id}: --border is ${border.toFixed(3)} against --background, `
    + `inside a surface ramp that reaches ${outermost.toFixed(3)}`).toBeGreaterThanOrEqual(outermost + 0.15)
    expect(border, `${id}: --border is only ${border.toFixed(3)} against --background`).toBeGreaterThanOrEqual(1.3)

    // An input outline delineates something you can type into. It is never
    // weaker than a decorative divider.
    const input = contrast(palette['--input']!, bg)
    expect(input, `${id}: --input (${input.toFixed(3)}) is weaker than --border (${border.toFixed(3)})`)
      .toBeGreaterThanOrEqual(border - 0.001)
  })

  it.each(ALL)('tints $id/$variant translucent background from its OWN background', ({ palette }) => {
    // TabBar.css.ts paints the tab strip in --lm-bg-translucent over the page,
    // so a value from another variant shows as a tab strip in the wrong colour
    // -- the one palette defect no contrast or structural case can see, because
    // a sibling's background is a perfectly valid colour.
    //
    // This is what a new variant gets wrong: eight of the eight added with the
    // variant model carried the sibling they were copied from, and Default
    // carried a plain white against a background of rgb(255 254 252).
    const bg = parseColor(palette['--background']!)!
    const translucent = parseColor(palette['--lm-bg-translucent']!)!
    expect(translucent, `--lm-bg-translucent is not --background at 50% alpha`).toEqual(bg)
    expect(palette['--lm-bg-translucent'], 'the alpha is not 0.5').toMatch(/,\s*0\.5\s*\)$/)
  })

  it.each(ALL)('dims $id/$variant text in one direction only', ({ palette }) => {
    // The three text weights must separate MONOTONICALLY from the background:
    // line numbers recede furthest, comments sit between, body text carries the
    // most contrast. The floors above pass a comment colour of pure white --
    // it clears 3:1 easily -- while inverting the hierarchy the reader uses to
    // tell a comment from code. Ayu Mirage shipped exactly that.
    const bg = palette['--background']!
    const faint = contrast(palette['--faint-foreground']!, bg)
    const muted = contrast(palette['--muted-foreground']!, bg)
    const body = contrast(palette['--foreground']!, bg)
    expect(muted, `--muted-foreground (${muted.toFixed(2)}:1) is not dimmer than --foreground (${body.toFixed(2)}:1)`)
      .toBeLessThanOrEqual(body)
    expect(faint, `--faint-foreground (${faint.toFixed(2)}:1) is not dimmer than --muted-foreground (${muted.toFixed(2)}:1)`)
      .toBeLessThanOrEqual(muted)
  })

  it.each(ALL)('separates $id/$variant recedes its surface ramp in one direction only', ({ palette }) => {
    // The mirror of the case above, for the surfaces rather than the text:
    // --secondary is the most distinct from the page, --faint the least. A
    // ramp out of order makes a nested panel read as the outer one.
    const bg = palette['--background']!
    const [secondary, muted, faint] = (['--secondary', '--muted', '--faint'] as const)
      .map(t => contrast(palette[t]!, bg))
    expect(secondary, 'the surface ramp is not ordered secondary > muted').toBeGreaterThanOrEqual(muted!)
    expect(muted, 'the surface ramp is not ordered muted > faint').toBeGreaterThanOrEqual(faint!)
  })

  it.each(ALL)('keeps $id/$variant danger and warning apart at a glance', ({ id, palette }) => {
    // Both are amber-to-red, and a small gap makes two states one state: the
    // git-status icons draw modified in --warning and deleted in --danger, side
    // by side in the same tree. Nord's light variant took nord12 for --warning,
    // which upstream reserves for annotations -- 20 degrees from nord11, the
    // tightest pair in the catalogue and the only one under 34.
    const gap = hueGap(palette['--danger']!, palette['--warning']!)
    expect(gap, `${id}: --danger and --warning are ${gap.toFixed(0)} degrees apart`).toBeGreaterThanOrEqual(30)
  })

  it.each(ALL)('tints a $id/$variant diff row visibly without swallowing the code on it', ({ id, variant, palette }) => {
    // The two jobs a diff row does at once, measured rather than pinned -- the
    // strengths in ~/styles/diffTint.ts may be retuned, but neither property
    // may be given up. The tint mixes the CODE surface's own --danger /
    // --success into that surface, so this measures what a reader sees.
    const surface = palette['--background']!
    for (const role of ['--danger', '--success']) {
      const row = mixOver(palette[role]!, DIFF_TINT[variant].row, surface)
      const word = mixOver(palette[role]!, DIFF_TINT[variant].word, row)

      // SEPARATION, as colour distance and NOT as contrast ratio. A contrast
      // ratio reads luminance alone, which understates a tint: Solarized Dark's
      // #dc322f row over its #002b36 surface is 1.04:1 and unmistakable, because
      // the whole signal is hue. Requiring a luminance ratio would fail that
      // row and push every theme toward tinting by lightness only.
      const rowGap = colorDistance(row, surface)
      expect(rowGap, `${id}: a ${role} row is only ${rowGap.toFixed(1)} from its surface`).toBeGreaterThanOrEqual(14)
      const wordGap = colorDistance(word, row)
      expect(wordGap, `${id}: a ${role} word-diff is only ${wordGap.toFixed(1)} from its row`).toBeGreaterThanOrEqual(14)

      // LEGIBILITY, as a fraction of what the plain surface offered. A floor
      // would be the wrong shape: a light theme whose own tokens sit at 4.38:1
      // cannot reach 4.5:1 on a tinted row however thin the tint, because the
      // UNTINTED surface does not reach it either. What the tint may not do is
      // take a large share of whatever the theme started with.
      const plain = contrast(palette['--foreground']!, surface)
      for (const [layer, bg] of [['row', row], ['word-diff', word]] as const) {
        const kept = contrast(palette['--foreground']!, bg) / plain
        expect(kept, `${id}: a ${role} ${layer} keeps only ${(kept * 100).toFixed(0)}% of the surface's contrast`)
          .toBeGreaterThanOrEqual(0.5)
        const absolute = contrast(palette['--foreground']!, bg)
        expect(absolute, `${id}: body text on a ${role} ${layer} is ${absolute.toFixed(2)}:1`).toBeGreaterThanOrEqual(3.5)
      }
    }
  })

  it('asks a light diff row for a different strength than a dark one', () => {
    // The whole reason the table exists. A dark surface moves further per unit
    // of tint, so one strength over-tints dark and under-serves light -- which
    // pays out of a much thinner margin. Equal numbers here would mean the
    // table had quietly been collapsed back to one.
    expect(DIFF_TINT.light.row).not.toBe(DIFF_TINT.dark.row)
    for (const polarity of VARIANTS) {
      for (const layer of ['row', 'word'] as const)
        expect(DIFF_TINT[polarity][layer], `${polarity}.${layer}`).toMatch(/^\d+%$/)
    }
  })

  it.each(ALL)('tints $id/$variant subtle fills with the hue they stand for', ({ palette }) => {
    // --lm-*-subtle is the low-emphasis form of its own semantic colour. It
    // carries the hue and drops the saturation; a subtle fill in a different
    // hue is a second answer to which colour the state is.
    for (const role of ['danger', 'success', 'warning']) {
      const gap = hueGap(palette[`--${role}`]!, palette[`--lm-${role}-subtle`]!)
      expect(gap, `--lm-${role}-subtle is ${gap.toFixed(0)} degrees off --${role}`).toBeLessThanOrEqual(18)
    }
  })
})

const ANSI_SLOTS = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'] as const
const CHROMATIC = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan'] as const
const ACHROMATIC = ['black', 'white', 'brightBlack', 'brightWhite'] as const

// The ANSI sets come from a community scheme collection rather than from each
// project directly, and their quality varies. `Atom One Light` in that
// collection collapses cyan onto green and black onto brightBlack -- it is not
// used, and these cases are what caught it.
describe('theme terminal palettes', () => {
  it('gives every theme all sixteen slots, as parseable colours', () => {
    for (const v of ALL_VARIANTS) {
      const ansi = v.terminal as unknown as Record<string, string>
      expect(Object.keys(ansi).sort(), `${v.id} slot set`).toEqual([...ANSI_SLOTS].sort())
      for (const slot of ANSI_SLOTS)
        expect(parseColor(ansi[slot]!), `${v.id}: ${slot}`).toBeDefined()
    }
  })

  it.each(ALL)('keeps $id/$variant chromatic slots distinct from each other', ({ id }) => {
    // Two ANSI slots sharing a colour is not a near-miss, it is a lost channel:
    // a program that prints cyan is indistinguishable from one printing green.
    const ansi = ALL.find(a => a.id === id)!.terminal
    const byColor = new Map<string, string[]>()
    for (const slot of CHROMATIC) {
      const key = ansi[slot]!.toLowerCase()
      byColor.set(key, [...(byColor.get(key) ?? []), slot])
    }
    for (const [color, slots] of byColor)
      expect(slots, `${slots.join(' and ')} both use ${color}`).toHaveLength(1)
  })

  it.each(ALL)('keeps $id/$variant dim text apart from black', ({ id }) => {
    const ansi = ALL.find(a => a.id === id)!.terminal
    expect(ansi.black!.toLowerCase()).not.toBe(ansi.brightBlack!.toLowerCase())
  })

  // A collapsed normal/bright pair is a LOST CHANNEL: a program that dims a row
  // to plain white, or that distinguishes SGR 37 from SGR 97, has no signal
  // left. Nine variants tolerate one, and every entry below is the upstream
  // scheme's own mapping -- read from the collection each theme file names, not
  // derived here, which `types.ts` forbids. Stated rather than left silent, so
  // the tolerance is argued: a NEW entry is a copy-paste error until someone
  // checks upstream, and an entry that disappears means a set changed.
  it('pins which variants let a bright slot repeat its normal one', () => {
    const PAIRS = [
      ['black', 'brightBlack'],
      ['red', 'brightRed'],
      ['green', 'brightGreen'],
      ['yellow', 'brightYellow'],
      ['blue', 'brightBlue'],
      ['magenta', 'brightMagenta'],
      ['cyan', 'brightCyan'],
      ['white', 'brightWhite'],
    ] as const
    const CHROMATIC = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan']

    const collapsed: Record<string, string[]> = {}
    for (const { id, terminal } of ALL) {
      const slots = PAIRS
        .filter(([normal, bright]) => terminal[normal]!.toLowerCase() === terminal[bright]!.toLowerCase())
        .map(([normal]) => normal)
      if (slots.length > 0)
        collapsed[id] = slots
    }

    expect(collapsed).toEqual({
      'nord-light': ['red', 'green', 'yellow', 'blue', 'magenta'],
      'nord-dark': ['red', 'green', 'yellow', 'blue', 'magenta'],
      'one-light': CHROMATIC,
      'one-dark': [...CHROMATIC, 'white'],
      'rose-pine-dawn': [...CHROMATIC, 'white'],
      'rose-pine-moon': [...CHROMATIC, 'white'],
      'rose-pine-main': [...CHROMATIC, 'white'],
      'tokyo-night-day': CHROMATIC,
      'tokyo-night-night': CHROMATIC,
    })
  })

  it.each(ALL)('leaves $id/$variant readable on its own terminal background', ({ id, palette }) => {
    const ansi = ALL.find(a => a.id === id)!.terminal
    const bg = palette['--background']!

    // At least ONE achromatic slot must carry plain text. Which one flips with
    // the scheme's convention: several light schemes (Catppuccin Latte, gruvbox
    // light, Rosé Pine Dawn, TokyoNight Day) deliberately invert, putting a
    // light surface in `black` and dark text in `white`, so a program written
    // for a dark terminal still reads. Requiring `black` specifically would
    // fail those for following their own upstream.
    const best = Math.max(...ACHROMATIC.map(s => contrast(ansi[s]!, bg)))
    expect(best, `no achromatic slot reaches 4.5:1 on ${bg}`).toBeGreaterThanOrEqual(4.5)

    // Every CHROMATIC slot must be visible. The achromatic four are exempt by
    // design, not by concession: one end of that ramp always sits near the
    // background -- it is what programs fill and shadow with -- and which end
    // depends on the scheme's convention. The case above already requires the
    // other end to carry readable text.
    for (const slot of [...CHROMATIC, 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan'] as const) {
      const ratio = contrast(ansi[slot]!, bg)
      expect(ratio, `${slot} is ${ratio.toFixed(2)}:1 on ${bg}`).toBeGreaterThanOrEqual(1.7)
    }
  })

  it.each(ALL.filter(a => !a.id.startsWith(`${DEFAULT_THEME_ID}-`)))('answers $id/$variant with one colour per state, palette and terminal alike', ({ id, palette, terminal }) => {
    // A theme states red, green and yellow twice: once for the app's semantic
    // fills and once for the terminal. They are the same question, so they must
    // not have different answers -- a repository whose modified files are amber
    // in the tree and olive in `git status` looks like two themes at once.
    //
    // The floor is a hue band, not equality: 46 of these 90 pairs ARE the same
    // colour, and the rest differ only in lightness, which each surface tunes
    // for its own background. Two real mismatches were found by it -- Tokyo
    // Night Day taking the escape teal for --success while Night took the
    // string green (82 degrees), and Nord's light --warning (26 degrees).
    //
    // The Default theme is exempt, and is the ONE theme that can be: its ANSI
    // set is Dimidium's, a different project from its palette, which is why it
    // alone carries a `terminalLabel`. Its green sits 20.5 degrees off.
    for (const [role, slot] of [['danger', 'red'], ['success', 'green'], ['warning', 'yellow']] as const) {
      const gap = hueGap(palette[`--${role}`]!, terminal[slot]!)
      expect(gap, `${id}: --${role} is ${gap.toFixed(0)} degrees off ANSI ${slot}`).toBeLessThanOrEqual(20)
    }
  })

  it('moves ANSI black with the variant wherever it is a background', () => {
    // Slot 0 is the one ANSI colour a scheme routinely sets TO its background,
    // so a program that fills with black paints nothing. gruvbox and Solarized
    // both do it, and it is correct.
    //
    // It is also the one slot that breaks when variants of a theme SHARE an
    // ANSI set. Gruvbox's six share fifteen slots rightly -- upstream's
    // contrast axis moves the background and nothing else -- but slot 0 is
    // `s:bg0`, that very background, so hard and soft inherited medium's and
    // landed 1.03:1 and 1.11:1 off their own: a smudge that reads as a display
    // fault rather than as either text or background.
    //
    // The rule is therefore about FOLLOWING, not about separation: a black that
    // matches some variant of its theme must match its own. It says nothing
    // about Ayu Dark or Tokyo Night, whose upstream blacks sit near a
    // background without being one.
    for (const theme of THEMES) {
      const backgrounds = new Set(theme.variants.map(v => v.palette['--background']!.toLowerCase()))
      for (const v of theme.variants) {
        const black = v.terminal.black.toLowerCase()
        if (!backgrounds.has(black))
          continue
        expect(black, `${v.id}: ANSI black is a SIBLING variant's background, not its own`)
          .toBe(v.palette['--background']!.toLowerCase())
      }
    }
  })

  it('credits the ANSI source separately from the palette source', () => {
    // They are different projects for every theme: the UI palettes were read
    // from each project directly, the ANSI sets from a scheme collection (and,
    // for Default, from Dimidium). Assuming them equal puts the wrong
    // attribution in NOTICE.
    for (const theme of THEMES) {
      expect(theme.terminalCredit, `${theme.id} has no terminal credit`).toBeDefined()
      expect(theme.terminalCredit!.url).toMatch(/^https:\/\//)
      expect(['MIT', 'Apache-2.0', 'Zlib'], `${theme.id} terminal license`)
        .toContain(theme.terminalCredit!.license)
    }
    const def = THEMES.find(t => t.id === DEFAULT_THEME_ID)!
    expect(def.terminalCredit!.project, 'Default\'s terminal is Dimidium').toBe('Dimidium')
  })
})

describe('themeById', () => {
  it('returns the named theme', () => {
    for (const theme of THEMES)
      expect(themeById(theme.id)).toBe(theme)
  })

  it('falls back to the default theme for a name this build does not carry', () => {
    // Reached by an ordinary path, not only by corruption: a preference written
    // by a newer build names a theme this one has never heard of.
    for (const name of ['', 'nope', 'Catppuccin', 'default ', undefined, null])
      expect(themeById(name), `themeById(${JSON.stringify(name)})`).toBe(THEMES[0])
  })
})

describe('isThemeId', () => {
  it('accepts every catalogued id and rejects everything else', () => {
    for (const theme of THEMES)
      expect(isThemeId(theme.id)).toBe(true)
    for (const name of ['', 'nope', 'Catppuccin', 'rose_pine'])
      expect(isThemeId(name), name).toBe(false)
  })
})

describe('resolveVariant fallbacks', () => {
  // Light variants are declared first in all eleven modules, so `variants[0]` is
  // never a valid answer for dark. A dangling id and a real id of the wrong
  // polarity are the two ways a hand-edited module reaches this path, and
  // matching on the id alone admitted both.
  it('answers the polarity asked for when defaults is broken', () => {
    for (const dark of ['no-such-variant', 'one-light']) {
      const broken = { ...oneTheme, defaults: { light: 'one-light', dark } }
      expect(resolveVariant(broken, undefined, 'dark').polarity, dark).toBe('dark')
    }
  })

  // The repair must not disturb the ordinary path.
  it('still answers a well-formed default exactly', () => {
    expect(resolveVariant(oneTheme, undefined, 'dark').id).toBe(oneTheme.defaults.dark)
    expect(resolveVariant(oneTheme, undefined, 'light').id).toBe(oneTheme.defaults.light)
  })

  // A theme with no variant of the polarity asked for cannot be answered at
  // all, and the catalogue is the only source of a ThemeDefinition, so it is a
  // defect in a theme module. The predecessor ended `?? theme.variants[0]!`:
  // for an empty `variants` the assertion handed back `undefined` typed as a
  // ThemeVariant, and the first read of `.palette` threw somewhere else --
  // inside a stylesheet builder, or inside the terminal -- with nothing in the
  // message that names the theme. The throw has to carry both facts a reader
  // needs to find the module.
  it('throws naming the theme when it declares no variant of the polarity', () => {
    const lightOnly = {
      ...oneTheme,
      variants: oneTheme.variants.filter(v => v.polarity === 'light'),
    }

    expect(() => resolveVariant(lightOnly, undefined, 'dark'))
      .toThrow(/"one".*no dark variant/)
    // A named variant of the right polarity that belongs to ANOTHER theme is
    // refused before the fallbacks run, so it reaches the throw too.
    expect(() => resolveVariant(lightOnly, 'nord-dark', 'dark'))
      .toThrow(/"one".*no dark variant/)
    // The polarity it does declare still resolves.
    expect(resolveVariant(lightOnly, undefined, 'light').polarity).toBe('light')
  })

  it('throws for a theme that declares no variants at all', () => {
    expect(() => resolveVariant({ ...oneTheme, variants: [] }, undefined, 'light'))
      .toThrow(/"one".*no light variant/)
  })
})

describe('paletteColorToHex', () => {
  // A palette colour that leaves CSS has to be readable by whatever consumes it.
  // xterm parses a hex literal or a COMMA-separated rgb(); Default states its
  // background, foreground, primary and accent as space-separated CSS Color 4,
  // which xterm cannot read -- it fell through to a canvas probe, and where no
  // 2D context exists parseColor swallowed the throw and painted black on white.
  it('converts the space-separated CSS Color 4 form', () => {
    expect(paletteColorToHex('rgb(255 254 252)')).toBe('#fffefc')
    expect(paletteColorToHex('rgb(12 12 11)')).toBe('#0c0c0b')
  })

  it('converts the comma-separated form too', () => {
    expect(paletteColorToHex('rgb(255, 254, 252)')).toBe('#fffefc')
  })

  it('pads a single-digit channel', () => {
    expect(paletteColorToHex('rgb(0 0 0)')).toBe('#000000')
    expect(paletteColorToHex('rgb(1 2 3)')).toBe('#010203')
  })

  // The ten adapted themes already state hex, so the common case is a no-op.
  it('passes hex through unchanged', () => {
    expect(paletteColorToHex('#abb2bf')).toBe('#abb2bf')
  })

  // A form this cannot read is returned as-is rather than refused: passing it to
  // a parser that may well accept it beats substituting a colour nobody chose.
  it('passes an unrecognised form through unchanged', () => {
    expect(paletteColorToHex('oklch(0.7 0.1 200)')).toBe('oklch(0.7 0.1 200)')
    expect(paletteColorToHex('')).toBe('')
  })

  // Every colour the terminal takes from the palette must survive the trip.
  it('yields a form xterm can parse for every variant', () => {
    for (const { id, palette } of ALL_VARIANTS.map(v => ({ id: v.id, palette: v.palette }))) {
      for (const token of ['--background', '--foreground', '--primary', '--accent'] as const) {
        const hex = paletteColorToHex(palette[token]!)
        expect(hex, `${id} ${token} is ${hex}`).toMatch(/^#[0-9a-f]{6}$/i)
      }
    }
  })
})
