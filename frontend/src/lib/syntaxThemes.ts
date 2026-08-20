import type { ThemeRegistrationRaw } from 'shiki/core'
import { DEFAULT_THEME_ID, resolveVariant, themeById } from '~/styles/themes'

/**
 * Loading the TextMate themes the catalogue names.
 *
 * WHICH theme each look highlights with lives on the variant itself
 * (`ThemeVariant.syntax`), so the palette and the highlighting cannot drift --
 * a second table here is exactly what used to let them. This module only knows
 * how to fetch a theme document by name, because a TextMate theme is a lazily
 * imported JSON chunk and `~/styles/themes/` must stay plain data that
 * `scripts/generate-notice.mjs` can read under bun.
 *
 * WHERE THESE COME FROM. Nine of the eleven themes load both halves from
 * `@shikijs/themes` -- the upstream projects' own editor themes, from the same
 * projects already credited for the UI palettes. Nord and Tokyo Night take
 * their dark half from there and their light halves from this directory,
 * because Shiki bundles no light theme for either. All three local files are
 * COPIES of an upstream document, and each one states its own edits:
 *
 *   - `tokyo-night-day` is Tokyo Night's own
 *     `themes/tokyo-night-light-color-theme.json`. All 116 `tokenColors`
 *     entries are upstream's, unchanged. Three fields differ: the theme name,
 *     the `type` (corrected to `light` -- the upstream file says `dark`, and
 *     Shiki reads `type` to resolve the default colour of a dual-theme
 *     render), and the `colors` map, reduced to the two keys Shiki reads.
 *   - `nord-light` and `nord-light-brighter` are the two flavours that
 *     huytd/vscode-nord-light publishes, and they pair with the two Nord light
 *     variants. Their `tokenColors` are upstream's at 80 and 82 rules, with
 *     one correction, in `nord-light` alone: upstream writes `#3B42527` --
 *     seven hex digits, which no parser reads as a colour -- on
 *     `meta.separator` and on `punctuation.section.embedded`, where every
 *     other rule of that shade spells nord1 `#3B4252`. Shiki loads the
 *     malformed value without complaint and emits it into a `color:`
 *     declaration that the browser then drops, so those two scopes silently
 *     inherit their parent's colour. `nord-light-brighter` carries no such
 *     value and is copied unchanged.
 *   - Both Nord files also GAIN the sixteen `terminal.ansi*` colours, which
 *     upstream ships for neither. An `ansi` code fence is coloured from those,
 *     and they come from the same variant's own terminal set, so a fence in
 *     chat matches the terminal beside it. `syntaxThemes.test.ts` pins that
 *     agreement.
 *
 * NOTICE credits each of these under the project it came from, whose MIT terms
 * cover the copy.
 *
 * DEFAULT BORROWS GITHUB'S, and that is deliberate rather than a gap. Dimidium
 * is a terminal scheme and ships no editor theme, so the Default theme has no
 * syntax theme of its own -- and `github-light`/`github-dark` is the pair
 * LeapMux highlighted every line of code with before this catalogue existed.
 * Keeping it means the default install looks the way it always has. A generated
 * Default pair lived here briefly and was dropped: it changed the colours of
 * every code block for every user who never opened this setting, to buy an
 * agreement with the terminal that nothing had asked for.
 */

/**
 * A light/dark pair of TextMate theme names, built from the two variants a
 * preference resolves to.
 *
 * Shiki's dual-theme output carries BOTH halves in every token, and CSS picks
 * between them by `data-theme` -- so this pair, not a single name, is what a
 * highlighter is pointed at. `~/lib/syntaxThemeStore` builds it.
 */
export interface SyntaxThemePair {
  light: string
  dark: string
}

/**
 * How to load each TextMate theme, as a dynamic import.
 *
 * Dynamic so a build ships one chunk per theme and a session downloads only the
 * pair it paints with. Registering all 28 up front would add roughly a
 * megabyte of JSON to the initial payload for colours all but two of which no
 * given user ever sees.
 */
// The value type is deliberately loose. A vendored VS Code theme document states
// `tokenColors`, while `ThemeRegistrationRaw` requires the `settings` spelling of
// the same thing; Shiki normalizes between them at load, but the JSON modules do
// not typecheck against the stricter shape.
const LOADERS: Record<string, () => Promise<{ default: unknown }>> = {
  // Vendored, in this repo.
  'nord-light': () => import('./syntaxThemes/nord-light.json'),
  'nord-light-brighter': () => import('./syntaxThemes/nord-light-brighter.json'),
  'tokyo-night-day': () => import('./syntaxThemes/tokyo-night-day.json'),
  // Upstream, via @shikijs/themes.
  'ayu-light': () => import('@shikijs/themes/ayu-light'),
  'ayu-dark': () => import('@shikijs/themes/ayu-dark'),
  'ayu-mirage': () => import('@shikijs/themes/ayu-mirage'),
  'catppuccin-latte': () => import('@shikijs/themes/catppuccin-latte'),
  'catppuccin-frappe': () => import('@shikijs/themes/catppuccin-frappe'),
  'catppuccin-macchiato': () => import('@shikijs/themes/catppuccin-macchiato'),
  'catppuccin-mocha': () => import('@shikijs/themes/catppuccin-mocha'),
  'everforest-light': () => import('@shikijs/themes/everforest-light'),
  'everforest-dark': () => import('@shikijs/themes/everforest-dark'),
  'github-light': () => import('@shikijs/themes/github-light'),
  'github-dark': () => import('@shikijs/themes/github-dark'),
  'gruvbox-light-hard': () => import('@shikijs/themes/gruvbox-light-hard'),
  'gruvbox-light-medium': () => import('@shikijs/themes/gruvbox-light-medium'),
  'gruvbox-light-soft': () => import('@shikijs/themes/gruvbox-light-soft'),
  'gruvbox-dark-hard': () => import('@shikijs/themes/gruvbox-dark-hard'),
  'gruvbox-dark-medium': () => import('@shikijs/themes/gruvbox-dark-medium'),
  'gruvbox-dark-soft': () => import('@shikijs/themes/gruvbox-dark-soft'),
  'nord': () => import('@shikijs/themes/nord'),
  'one-light': () => import('@shikijs/themes/one-light'),
  'one-dark-pro': () => import('@shikijs/themes/one-dark-pro'),
  'rose-pine-dawn': () => import('@shikijs/themes/rose-pine-dawn'),
  'rose-pine-moon': () => import('@shikijs/themes/rose-pine-moon'),
  'rose-pine': () => import('@shikijs/themes/rose-pine'),
  'solarized-light': () => import('@shikijs/themes/solarized-light'),
  'solarized-dark': () => import('@shikijs/themes/solarized-dark'),
  'tokyo-night': () => import('@shikijs/themes/tokyo-night'),
}

/** Every TextMate theme name this build can load. */
export function isSyntaxThemeName(name: string): boolean {
  return Object.hasOwn(LOADERS, name)
}

/**
 * The same set, as a list.
 *
 * Exported for `syntaxThemes.test.ts`, which pins it against the names
 * every catalogued VARIANT names, so neither list can hold an entry the other
 * lacks. A
 * name with no loader paints the fallback theme's colours under another
 * theme's name; a loader nothing names ships a theme document no user reaches.
 */
export const LOADABLE_SYNTAX_THEMES: readonly string[] = Object.keys(LOADERS)

/**
 * The pair a UI theme id highlights with, taking each half from that theme's
 * DEFAULT variant of the matching polarity.
 *
 * The catalogue is the single source: a variant states its own `syntax` name,
 * so this list cannot drift from the palettes the way a second table did.
 */
export function syntaxPairFor(themeId: string): SyntaxThemePair {
  const theme = themeById(themeId)
  return {
    light: resolveVariant(theme, undefined, 'light').syntax,
    dark: resolveVariant(theme, undefined, 'dark').syntax,
  }
}

const loaded = new Map<string, Promise<ThemeRegistrationRaw>>()

/**
 * Force a theme document's background transparent, and register it under `name`.
 *
 * EVERY theme this app registers goes through here -- the lazily imported ones
 * in LOADERS above, and the two the synchronous highlighter carries in the main
 * bundle (see `~/lib/renderMarkdown`) -- so the rule cannot hold on one path and
 * not the other. `main` had that single authority in `transparentBgThemes`; the
 * split into two hand-applied copies is what this restores.
 *
 * The app's own wrapper owns the block background, which is why the document's
 * must go: a baked-in background would sit under the surface the code palette
 * paints and win.
 */
export function withTransparentBg(doc: unknown, name?: string): ThemeRegistrationRaw {
  return { ...(doc as object), ...(name === undefined ? {} : { name }), bg: 'transparent' } as ThemeRegistrationRaw
}

/**
 * Load one TextMate theme, with its background forced transparent so the app's
 * own wrapper owns the block background.
 *
 * Memoized by name: the same theme is asked for by the two workers' highlighters
 * and the editor's, and a theme is immutable once loaded.
 */
export function loadSyntaxTheme(name: string): Promise<ThemeRegistrationRaw> {
  const cached = loaded.get(name)
  if (cached)
    return cached
  // An unknown name falls back to the DEFAULT theme's dark half, which is the
  // same pair `syntaxPairFor` answers an unknown theme id with. Named directly
  // rather than reached through an empty-string sentinel that only worked
  // because `themeById` does not recognise it -- a reader had to follow two
  // functions to learn which theme the fallback actually is.
  // `Object.hasOwn`, not a bare bracket read: LOADERS is a plain object, so `[]`
  // answers with an INHERITED member for `constructor`, `toString` or
  // `__proto__` -- a truthy value that the `??` below would then keep, and
  // calling it throws inside the load instead of taking the fallback. The
  // sibling `isSyntaxThemeName` and `resolveBundledLang` ask the same way.
  const loader = Object.hasOwn(LOADERS, name)
    ? LOADERS[name]!
    : LOADERS[syntaxPairFor(DEFAULT_THEME_ID).dark]!
  // `name` is stamped on unconditionally. Shiki registers a theme under the
  // DOCUMENT's own name, so the fallback above registered itself as
  // `github-dark` and the name that was asked for stayed permanently absent:
  // `ensureThemesRegistered` re-loaded and re-registered on every call, the
  // editor's `areThemesLoaded` never converged, and every `codeToTokens`
  // naming that half threw. Stamping it makes "the theme this call loads is
  // registered under the name this call asked for" a property rather than a
  // coincidence of the loader keys matching the upstream documents' names.
  const promise = loader().then(m => withTransparentBg(m.default ?? m, name))
  // Drop a REJECTED load so a later call retries. Caching the rejection would
  // leave a transient chunk-import failure permanently unhighlighted.
  void promise.catch(() => loaded.delete(name))
  loaded.set(name, promise)
  return promise
}

/** Load both halves of a pair. */
export function loadSyntaxPair(pair: SyntaxThemePair): Promise<ThemeRegistrationRaw[]> {
  return Promise.all([loadSyntaxTheme(pair.light), loadSyntaxTheme(pair.dark)])
}

/**
 * The minimum of `HighlighterCore` this module drives. Declared structurally so
 * the sync and async highlighters, which differ in how they are built but not in
 * how a theme is registered, share one registrar.
 */
export interface ThemeRegistrar {
  getLoadedThemes: () => string[]
  loadTheme: (theme: ThemeRegistrationRaw) => Promise<void>
}

/**
 * Register both halves of `pair` on `hl`, if they are not already.
 *
 * Registration is ADDITIVE and never unloads: Shiki keys a tokenized result by
 * theme name, so leaving an old pair registered costs a little memory and buys
 * an instant switch back. Unloading would also break any render still in flight
 * under the previous pair.
 */
export async function ensureThemesRegistered(hl: ThemeRegistrar, pair: SyntaxThemePair): Promise<void> {
  const have = new Set(hl.getLoadedThemes())
  // Through a Set, because the two halves are the SAME name whenever the syntax
  // mode is pinned -- `resolveSyntaxPair` collapses them on purpose, since Shiki
  // carries both halves in every token and CSS picks between them. A plain
  // filter loaded and re-registered one document twice, on every highlighter,
  // on every switch, for exactly the configuration pinning exists to serve.
  const missing = [...new Set([pair.light, pair.dark])].filter(name => !have.has(name))
  if (missing.length === 0)
    return
  const themes = await Promise.all(missing.map(loadSyntaxTheme))
  for (const theme of themes)
    await hl.loadTheme(theme)
}
