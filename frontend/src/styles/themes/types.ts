// The shapes every theme in this directory is written to.
//
// PLAIN DATA, NO IMPORTS OUTSIDE THIS DIRECTORY. Two consumers need these
// values and only one of them can execute CSS: ~/styles/global.css.ts spreads
// each palette into its theme selectors, and scripts/generate-notice.mjs
// inlines the default palette into the standalone NOTICE.html page. That
// script runs under bun with no Vite and no vanilla-extract, so it resolves
// neither a `.css.ts` module nor the `~/` alias -- hence plain `.ts` files
// that import only their siblings by relative path.
//
// Typography is deliberately NOT here. The app wires --font-sans/--font-mono
// through `var(--ui-font-family, ...)` so a user preference can override them;
// the notice page has no preference store, so it declares its own. Each owner
// states its own fonts next to where it uses them.

/** Whether the app paints its light palette, its dark palette, or follows the OS. */
export type ThemeMode = 'system' | 'light' | 'dark'

/** The resolved half of a `ThemeMode`, after `system` is answered. */
export type ResolvedThemeMode = 'light' | 'dark'

/**
 * Which variant a preference pins for each polarity, by variant id.
 *
 * KEYED BY POLARITY, and both halves are stored even though the control shows
 * one at a time: `mode: 'system'` has to answer for both, because the OS flips
 * at dusk and the app must already know which dark variant to paint.
 *
 * An absent half means "this theme's default for that polarity". A half naming
 * a variant of another theme, or of the wrong polarity, is dropped on read --
 * see `parseThemeValue`.
 */
export interface ThemeVariantChoice {
  light?: string
  dark?: string
}

/** The whole appearance preference: which palette, which polarity, which variant. */
export interface ThemeValue {
  name: string
  mode: ThemeMode
  variant?: ThemeVariantChoice
}

/**
 * The sentinel that ties the TERMINAL and SYNTAX themes to the UI theme.
 *
 * A stored VALUE, not an absence. "Follow the UI" has to survive the UI
 * changing, so it cannot be recorded as a copy of whatever the UI happened to
 * hold at the time -- a user whose terminal keeps tracking the app after they
 * switch palettes is the default case, not the exception.
 *
 * It fills BOTH halves of a `TerminalThemeValue` or neither of them.
 */
export const MATCH_UI = 'match-ui'
export type MatchUi = typeof MATCH_UI

/** The terminal's palette: a theme id, or "follow the UI theme". */
export type TerminalThemeName = string | MatchUi
/** The terminal's mode: a mode, or "follow the UI's RESOLVED mode". */
export type TerminalThemeMode = ThemeMode | MatchUi

/**
 * The terminal or syntax appearance preference.
 *
 * The same shape as `ThemeValue`, deliberately, because one control renders all
 * three -- with one state the UI theme does not have: the whole choice can
 * follow the UI theme.
 *
 * INVARIANT: `name` holds `match-ui` if and only if `mode` holds it. The two
 * halves are ONE decision, which the control puts behind one "Match UI theme"
 * switch, so a document with the sentinel in one half only is not a third
 * state -- it is a document nothing produces.
 *
 * The halves used to move on their own. That put "Match UI" in the palette list
 * AND in the mode pills, which reads as two settings for one concept, and it
 * spelled four combinations of which two describe nothing a user asks for.
 * `parseTerminalThemeValue` repairs a mixed document, and the hub refuses to
 * store one.
 */
export interface TerminalThemeValue {
  name: TerminalThemeName
  mode: TerminalThemeMode
  variant?: ThemeVariantChoice
}

/**
 * One variant's CSS custom properties, keyed by property name (`--background`).
 *
 * Every theme states the same token set as the default theme's matching
 * variant, which `themes.test.ts` enforces. A missing token does not fall back
 * to another LeapMux theme -- it falls back to Oat's own value for that
 * property, which is a hole that only shows up after a theme switch.
 */
export type ThemePalette = Record<string, string>

/**
 * The sixteen ANSI colours a terminal needs, by xterm's own slot names.
 *
 * Background, foreground, cursor and selection are NOT here. They come from the
 * same theme's `palette`, so one theme states one background -- keeping a
 * second copy beside the ANSI set is exactly what let Default's terminal
 * background (`#fdfcfa`) drift from its `--background` (`rgb(255 254 252)`).
 */
export interface AnsiPalette {
  black: string
  red: string
  green: string
  yellow: string
  blue: string
  magenta: string
  cyan: string
  white: string
  brightBlack: string
  brightRed: string
  brightGreen: string
  brightYellow: string
  brightBlue: string
  brightMagenta: string
  brightCyan: string
  brightWhite: string
}

/** Where a third-party palette came from, for NOTICE and for the theme file's own header. */
export interface ThemeCredit {
  /** The upstream project's name, as its authors spell it. */
  project: string
  /** The upstream repository the values were read from. */
  url: string
  /** SPDX identifier. Every theme here is permissive. */
  license: string
}

/**
 * One painted look: a palette, a terminal set and a syntax theme that belong
 * together.
 *
 * THE UNIT THAT OWNS COLOUR. All three appearance surfaces resolve a variant
 * and then read one field of it, instead of each keeping its own light/dark
 * lookup. That is what lets a theme offer more than two looks: Catppuccin
 * publishes four flavours, of which one is light and three are dark.
 */
export interface ThemeVariant {
  /**
   * Stable, globally unique id: `<themeId>-<slug>`, kebab-case, where the slug
   * is the upstream project's own codename. Rosé Pine's main variant is
   * therefore `rose-pine-main`, not `rose-pine-rose-pine`.
   *
   * `data-ui-light` / `data-ui-dark` carry this and the stored preference names
   * it. It is NOT the Shiki theme name; see `syntax` below.
   */
  id: string
  /** The name shown in the variant picker: `Macchiato`, `Soft`. */
  label: string
  /**
   * Which half of the light/dark switch this variant answers for.
   *
   * Taken from the upstream project's own declaration -- Catppuccin's
   * `palette.json` states `dark: boolean` per flavour, and every theme
   * `@shikijs/themes` bundles states `type`. Never inferred from the
   * background's luminance, which would guess where upstream is explicit.
   */
  polarity: ResolvedThemeMode
  /** This variant's CSS custom properties. */
  palette: ThemePalette
  /** This variant's sixteen ANSI colours. */
  terminal: AnsiPalette
  /**
   * The TextMate theme name this variant highlights with.
   *
   * Decoupled from `id` on purpose: `rose-pine-main` highlights with the theme
   * Shiki calls `rose-pine`, and `nord-light` highlights with a document this
   * repo vendors. A plain string, so these modules stay import-free.
   */
  syntax: string
}

export interface ThemeDefinition {
  /** Stable id. Kebab-case; this is what `data-ui-theme` and the stored preference carry. */
  id: string
  /** The name shown in the theme picker. */
  label: string
  /**
   * The name this theme goes by in the TERMINAL picker, when its terminal
   * palette is a different project's.
   *
   * Only the Default theme sets it. Its sixteen ANSI colours are Dimidium's,
   * and a picker that lists them as "Default" leaves a user who wants Dimidium
   * unable to find the palette they are already looking at. The other ten
   * themes supply their own ANSI set, so the theme's name is the palette's
   * name and this stays absent.
   */
  terminalLabel?: string
  /**
   * The same, for the SYNTAX picker.
   *
   * Only the Default theme sets it, for the same reason: Dimidium ships no
   * editor theme, so Default highlights with GitHub's -- the pair LeapMux used
   * before this catalogue existed. `syntaxThemes.test.ts` pins that this name
   * agrees with the variants it actually points at.
   */
  syntaxLabel?: string
  /**
   * Every look this theme offers, with at least one of each polarity.
   *
   * `themes.test.ts` enforces both halves: a theme with no light variant could
   * not answer a light `data-theme`, and one with no dark variant could not
   * answer a dark OS.
   */
  variants: ThemeVariant[]
  /**
   * The variant each polarity resolves to when the preference names none.
   *
   * Every entry is the palette that theme shipped before variants existed, so
   * an account that never opens the picker sees no change. A golden list in
   * `themes.test.ts` pins that.
   */
  defaults: {
    light: string
    dark: string
  }
  /**
   * What the upstream project calls its variant axis -- Catppuccin says
   * "Flavor", Gruvbox says "Contrast". It becomes the picker's accessible name,
   * so the two adjacent menus in one row do not share one.
   *
   * Absent for a theme with a single variant per polarity, which renders no
   * variant picker at all.
   */
  variantLabel?: string
  /** Absent for the built-in default theme, present for every adapted one. */
  credit?: ThemeCredit
  /**
   * Where the ANSI set came from, recorded APART from `credit` because it is a
   * different project for every theme: the UI palettes were read from each
   * project directly, the ANSI sets from a terminal-scheme collection (and, for
   * Default, from Dimidium). Assuming the two equal would put the wrong
   * attribution in NOTICE.
   */
  terminalCredit?: ThemeCredit
}

// How an upstream editor palette maps onto LeapMux's tokens.
//
// Stated once, applied by every theme file, so the eleven palettes read as one
// system instead of eleven independent judgement calls. Where an upstream theme
// states no colour for a role, the theme file derives one from a neighbouring
// ramp step and says so at the site.
//
//   --background / --foreground        editor background / editor text
//   --card / --card-foreground         panel or sidebar background / its text
//   --secondary, --muted, --faint      the surface ramp, from most contrast to
//                                      least against --background
//   --muted-foreground                 comment colour
//   --faint-foreground                 line numbers / the dimmest readable text
//   --border, --input                  a step BEYOND the surface ramp, not a
//                                      member of it -- see below
//   --primary, --ring                  the theme's signature accent
//   --accent                           selection background
//   --accent-foreground                --foreground (selection keeps body text)
//   --danger / --success / --warning   red / green / yellow
//   --lm-*-subtle                      that hue mixed most of the way to --background
//   --lm-bg-translucent                --background at 50% alpha
//   --lm-icon-monochrome               between --muted-foreground and --foreground
//
// --border SITS OUTSIDE THE SURFACE RAMP. A border is drawn on the page, on a
// card and on a secondary surface, so measuring it against --background alone
// says nothing: an edge that reads on the page disappears on a panel. It must
// clear the OUTERMOST of --card and --secondary by the margin the default
// theme uses, and --input is never weaker than --border. Most upstream projects
// state a border that is a member of their surface ramp rather than a step past
// it -- Solarized's is base02, its own panel colour -- so 24 of the 30 variants
// carry a value moved outward in lightness alone, keeping the hue and the
// saturation their project states. themes.test.ts measures this per variant.
//
// Contrast is a hard floor, not a preference: themes.test.ts fails a variant
// whose --foreground does not reach 4.5:1 against --background, or whose
// --muted-foreground does not reach 3:1. An upstream comment colour that misses
// the floor is darkened (light variants) or lightened (dark variants) until it
// passes, and the theme file records the adjustment.
//
// NINE OF THESE TOKENS ALSO HAVE TO STAY APART FROM EACH OTHER. The theme
// picker previews a palette as a 3x3 chip, so components/common/ThemeSwatch.tsx
// draws --primary, --accent, --danger, --success, --warning, --border, --input,
// --lm-icon-monochrome and --lm-success-subtle on --background. Its suite fails
// a variant that puts two look-alike colours in one row or column of that chip,
// or that lets one of the nine sink into its own background. SWATCH_TOKENS in
// that file carries the measured floors and why these nine were chosen.
//
// A SECOND VARIANT OF ONE POLARITY -- another Catppuccin flavour, another
// Gruvbox contrast level -- is derived through this same table, from its own
// upstream values, and never by eye. Two rules make it reproducible:
//
//   - Whatever the variant's own project states, TAKE. Background, text,
//     accents and the sixteen ANSI colours are read from upstream, not mixed.
//   - Whatever the project does not state, DERIVE at the ratio its sibling
//     variant of the same polarity already uses, so a theme keeps its own
//     character across its variants instead of drifting to a house average.
//
// A contrast level moves the background out from under a value that cleared the
// floor for its sibling, so the floors are re-checked per variant and the theme
// file records any repair. Gruvbox's two soft variants needed one, for a border
// that the darker background had pulled under 1.1:1.
