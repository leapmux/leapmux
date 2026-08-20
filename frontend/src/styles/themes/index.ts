// The theme catalogue: the one list of palettes the app offers.
//
// THIS LIST IS THE SOURCE OF TRUTH FOR WHICH THEMES EXIST. The hub validates a
// stored theme name as a slug and nothing more, deliberately: the palettes are
// TypeScript modules the hub cannot see, so a Go copy of these ids would be a
// second authority that drifts the first time someone adds a file here.
// `themeById` answers for a name this build does not carry.
//
// See ./types.ts for the token roles, the contrast floors, and why every file
// in this directory is plain data with no imports from outside it.

import type { ResolvedThemeMode, ThemeDefinition, ThemeVariant, ThemeVariantChoice } from './types'
import { ayuTheme } from './ayu'
import { catppuccinTheme } from './catppuccin'
import { defaultTheme } from './default'
import { everforestTheme } from './everforest'
import { githubTheme } from './github'
import { gruvboxTheme } from './gruvbox'
import { nordTheme } from './nord'
import { oneTheme } from './one'
import { rosePineTheme } from './rose-pine'
import { solarizedTheme } from './solarized'
import { tokyoNightTheme } from './tokyo-night'
import { MATCH_UI } from './types'

export type {
  AnsiPalette,
  MatchUi,
  ResolvedThemeMode,
  TerminalThemeMode,
  TerminalThemeName,
  TerminalThemeValue,
  ThemeCredit,
  ThemeDefinition,
  ThemeMode,
  ThemePalette,
  ThemeValue,
  ThemeVariant,
  ThemeVariantChoice,
} from './types'
export { MATCH_UI } from './types'

/** The theme every fallback resolves to, and the palette `:root` carries. */
export const DEFAULT_THEME_ID = defaultTheme.id

/**
 * Every theme, in the order the picker lists them: Default first, then the
 * adapted themes alphabetically. The picker renders this list directly, so the
 * order here is the order a user sees.
 */
export const THEMES: readonly ThemeDefinition[] = [
  defaultTheme,
  ayuTheme,
  catppuccinTheme,
  everforestTheme,
  githubTheme,
  gruvboxTheme,
  nordTheme,
  oneTheme,
  rosePineTheme,
  solarizedTheme,
  tokyoNightTheme,
]

const BY_ID = new Map(THEMES.map(theme => [theme.id, theme]))

/** Whether `id` names a theme this build carries. */
export function isThemeId(id: string): boolean {
  return BY_ID.has(id)
}

/**
 * The theme `id` names, or the default theme.
 *
 * Never throws and never returns undefined. An unknown name reaches this
 * function on two ordinary paths -- a preference written by a newer build, and
 * a hand-edited localStorage document -- and neither is a reason to leave the
 * app with no palette at all.
 */
export function themeById(id: string | undefined | null): ThemeDefinition {
  return (id === undefined || id === null ? undefined : BY_ID.get(id)) ?? defaultTheme
}

/**
 * Which of the three appearance settings a picker is choosing for.
 *
 * The three offer the SAME eleven palettes, so a user chooses "Catppuccin" once
 * and means it everywhere. They do not always offer the same NAMES: a theme
 * whose terminal or syntax palette is a different project's says so, which is
 * how Dimidium and GitHub stay findable under a theme called Default.
 */
export type ThemeSurface = 'ui' | 'terminal' | 'syntax'

/** The name `theme` goes by in the picker for `surface`. */
export function themeLabel(theme: ThemeDefinition, surface: ThemeSurface): string {
  if (surface === 'terminal')
    return theme.terminalLabel ?? theme.label
  if (surface === 'syntax')
    return theme.syntaxLabel ?? theme.label
  return theme.label
}

/**
 * Every variant of every theme: themes in picker order, and each theme's
 * variants in the order its file declares them, light to dark.
 *
 * The two CSS emission loops iterate the split lists rather than this one,
 * because a light variant and a dark variant need different selectors.
 */
export const ALL_VARIANTS: readonly ThemeVariant[] = THEMES.flatMap(theme => theme.variants)
export const LIGHT_VARIANTS: readonly ThemeVariant[] = ALL_VARIANTS.filter(v => v.polarity === 'light')
export const DARK_VARIANTS: readonly ThemeVariant[] = ALL_VARIANTS.filter(v => v.polarity === 'dark')

const VARIANT_BY_ID = new Map(ALL_VARIANTS.map(v => [v.id, v]))

/** The variant `id` names, or `undefined`. Total; never throws. */
export function variantById(id: string | undefined | null): ThemeVariant | undefined {
  return id === undefined || id === null ? undefined : VARIANT_BY_ID.get(id)
}

/**
 * The variants `theme` offers for one polarity, in declaration order.
 *
 * The picker renders this list, and renders nothing at all when it holds one
 * entry: a drop-down with a single option is not a choice.
 */
export function variantsFor(theme: ThemeDefinition, polarity: ResolvedThemeMode): ThemeVariant[] {
  return theme.variants.filter(v => v.polarity === polarity)
}

/**
 * The variant a preference resolves to for one polarity.
 *
 * THE ONE LOOKUP ALL THREE SURFACES SHARE. The UI reads the result's `palette`,
 * the terminal its `terminal`, the highlighter its `syntax` — so a theme's
 * three faces cannot disagree about which look is showing.
 *
 * A named variant must match the polarity as well as the id, and must belong to
 * this theme. `{ dark: 'catppuccin-latte' }` names a real variant that cannot
 * answer for dark, and painting a light palette under `data-theme="dark"` is a
 * worse answer than the theme's own default. Both that and an unknown id fall
 * back to `defaults`, which is the palette the theme shipped before variants
 * existed.
 */
export function resolveVariant(
  theme: ThemeDefinition,
  wanted: string | undefined,
  polarity: ResolvedThemeMode,
): ThemeVariant {
  const named = wanted === undefined ? undefined : VARIANT_BY_ID.get(wanted)
  if (named && named.polarity === polarity && theme.variants.includes(named))
    return named
  // A default that DANGLES, or that names a real variant of the wrong polarity,
  // is answered by any variant of the polarity asked for -- never by
  // `variants[0]`. Every theme declares its light variants first, so
  // `variants[0]` would paint a light palette under `data-theme="dark"`, which
  // is the exact failure this function's doc says it exists to prevent. Matching
  // on the id ALONE let both classes through. themes.test.ts pins `defaults`, so
  // neither fallback runs for a catalogued theme.
  const fallback = theme.variants.find(v => v.id === theme.defaults[polarity] && v.polarity === polarity)
    ?? theme.variants.find(v => v.polarity === polarity)
  // A theme that declares NO variant of this polarity cannot be answered, and
  // the catalogue is the only source of a ThemeDefinition, so this is a defect
  // in a theme module rather than bad input. The predecessor ended `??
  // theme.variants[0]!`, and the assertion was untrue twice over: for an empty
  // `variants` it handed back `undefined` typed as a ThemeVariant, and the
  // first read of `.palette` -- in a stylesheet builder, or in the terminal --
  // threw with nothing in the message that names the theme. Throw at the cause.
  if (!fallback)
    throw new Error(`theme "${theme.id}" declares no ${polarity} variant`)
  return fallback
}

/**
 * A palette colour as a `#rrggbb` hex triple.
 *
 * The catalogue states colour in two forms: the ten adapted themes use hex,
 * and Default uses the space-separated CSS Color 4 form (`rgb(255 254 252)`).
 * CSS reads both, so the difference is invisible in a stylesheet -- but a
 * consumer OUTSIDE CSS does not necessarily read the second one. xterm's
 * `css.toColor` matches a hex literal or a COMMA-separated `rgb()` and falls
 * through to a canvas probe otherwise, and its `parseColor` swallows the throw
 * and substitutes plain black on white; a web-app manifest parser that rejects
 * the form falls back to the browser default. So every consumer that hands a
 * palette colour to something other than CSS normalizes it here.
 *
 * A value already in hex, or in a form this cannot read, is returned unchanged:
 * the ten adapted themes need no conversion, and refusing an unrecognised form
 * would be a worse answer than passing it to a parser that may well accept it.
 */
export function paletteColorToHex(value: string): string {
  const m = /^rgb\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)\s*\)$/.exec(value.trim())
  if (!m)
    return value
  return `#${m.slice(1).map(n => Number(n).toString(16).padStart(2, '0')).join('')}`
}

/**
 * Which theme supplies a derived surface, and whose variant choice goes with it.
 *
 * The terminal and the code surface each carry a preference that may hold the
 * `match-ui` sentinel, and both then have to answer the same two questions: is
 * this row following the app, and if so whose `variant` applies? That pair of
 * lines was written out three times across two modules, and the failure it
 * guards against is subtle -- reading the APP's variant under a DETACHED palette
 * names a variant of the wrong theme, which `resolveVariant` then silently
 * discards for that theme's default, so the row wears a look the user never
 * chose and no error says why.
 *
 * `following` is returned as well as used, because every caller branches on it
 * again for the MODE, which each resolves its own way.
 */
export function resolveThemeSelection(
  pref: { name: string, variant?: ThemeVariantChoice },
  ui: { name: string, variant?: ThemeVariantChoice },
): { following: boolean, theme: ThemeDefinition, chosen: ThemeVariantChoice | undefined } {
  const following = pref.name === MATCH_UI
  return {
    following,
    theme: themeById(following ? ui.name : pref.name),
    chosen: following ? ui.variant : pref.variant,
  }
}
