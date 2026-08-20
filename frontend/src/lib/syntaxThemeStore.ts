import type { SyntaxThemePair } from './syntaxThemes'
import type { ResolvedThemeMode, TerminalThemeValue, ThemeValue, ThemeVariant } from '~/styles/themes'
import { createRoot, createSignal } from 'solid-js'
import { MATCH_UI, resolveThemeSelection, resolveVariant } from '~/styles/themes'
import { sameSyntaxPair, setSyntaxThemePair, syntaxThemePair } from './shikiThemes'
import { ensureThemesRegistered } from './syntaxThemes'

/**
 * Switching the live syntax theme, and telling the app when to re-render.
 *
 * A syntax theme cannot be swapped in CSS. Shiki bakes the resolved colours into
 * each token span at tokenize time, so the only way to repaint highlighted code
 * is to tokenize it again -- which is why this is a store with a generation
 * counter rather than another set of custom properties.
 *
 * The order below is the whole correctness argument:
 *
 *   1. REGISTER the new pair on the synchronous highlighter, awaiting the chunk
 *      import. Nothing observable changes yet.
 *   2. POINT the shared options at it (`setSyntaxThemePair`).
 *   3. INVALIDATE every cache holding tokenized output, then bump `generation`.
 *
 * Registering first is what makes step 2 safe: a synchronous call site
 * (`ansiTokenize`, the editor, the tool renderers) cannot await, so between 2
 * and 1 it would name a theme Shiki has not loaded and throw. Doing it in this
 * order means the old theme simply stays live a moment longer.
 */

/** Cache-clearing hooks, registered by the modules that own each cache. */
type Invalidator = () => void
const invalidators = new Set<Invalidator>()

/**
 * Register a cache to be dropped when the syntax theme changes.
 *
 * Inverted rather than imported: this module would otherwise have to import the
 * markdown renderer and the token cache, and both already import the theme
 * state -- a cycle. The owners opt in instead.
 */
export function onSyntaxThemeChange(invalidate: Invalidator): void {
  invalidators.add(invalidate)
}

function createSyntaxThemeStore() {
  // Bumped after every applied change. Consumers that hold rendered output read
  // it so their memo re-runs; nothing reads its VALUE, only its identity.
  const [generation, setGeneration] = createSignal(0)

  /**
   * Point the app at `pair`, loading it first.
   *
   * Serialized through `inFlight`: two quick changes must not interleave their
   * register and point steps, or the second could publish a pair whose themes
   * the first had not finished registering.
   */
  let inFlight: Promise<void> = Promise.resolve()

  async function apply(pair: SyntaxThemePair, registrar: Parameters<typeof ensureThemesRegistered>[0]): Promise<void> {
    if (sameSyntaxPair(syntaxThemePair(), pair))
      return
    await ensureThemesRegistered(registrar, pair)
    if (!setSyntaxThemePair(pair))
      return
    for (const invalidate of invalidators)
      invalidate()
    setGeneration(n => n + 1)
  }

  return {
    /**
     * Read by anything holding tokenized output, so a theme change re-renders it.
     * The chat's markdown and code rows, and the editor, all depend on this.
     */
    generation,
    /**
     * Point the app at `pair`.
     *
     * The returned promise REJECTS when the change fails, and the serialized
     * chain keeps running either way. A bare `.catch(() => {})` here swallowed
     * the one failure this module expects -- a lazy theme chunk whose `import()`
     * 404s or drops -- so the code surface silently never repainted, with no
     * console error, no retry and no trail: `syntaxThemes` even drops a rejected
     * load precisely so a later call can retry, and nothing ever made one.
     * The chain is advanced with a SEPARATE swallowing link so one failure does
     * not wedge every later change, while the promise handed back to the caller
     * still carries the error.
     */
    setSyntaxTheme(pair: SyntaxThemePair, registrar: Parameters<typeof ensureThemesRegistered>[0]): Promise<void> {
      const applied = inFlight.then(() => apply(pair, registrar))
      inFlight = applied.catch(() => {})
      return applied
    },
  }
}

export const syntaxThemeStore = createRoot(createSyntaxThemeStore)

export const { generation: syntaxThemeGeneration, setSyntaxTheme } = syntaxThemeStore

/**
 * Resolve the syntax preference against the UI theme to the pair to tokenize
 * with.
 *
 * Shiki's dual-theme output carries BOTH halves of a pair in every token
 * (`--shiki-light` and `--shiki-dark`), and CSS picks between them by
 * `data-theme`. So there are two answers, and the preference's NAME chooses:
 *
 *   - `match-ui` emits the UI theme's whole pair. Which variant shows then
 *     costs nothing to switch and needs no re-tokenization, because the app's
 *     own `data-theme` already selects it.
 *   - a pinned palette emits its ONE variant in both halves, so whichever
 *     variable `data-theme` selects carries the same colours. That is what lets
 *     a pinned mode beat the app's: code stays dark inside a light app.
 *
 * `systemMode` is the OS's own answer, not the app's resolved one. `system`
 * here means what it means in the Theme row -- follow the OS -- and reading the
 * app's mode instead would make the two rows disagree about one word. The
 * control never leaves a user on that combination by accident: turning "Match
 * UI theme" off seeds both halves from the UI theme, so a pinned app hands over
 * its pinned mode rather than `system`.
 */
export function resolveSyntaxPair(
  pref: TerminalThemeValue,
  ui: ThemeValue,
  systemMode: ResolvedThemeMode,
): SyntaxThemePair {
  // Which theme supplies the pair, and whose variant choice goes with it.
  const { following, theme, chosen } = resolveThemeSelection(pref, ui)
  const pairOf = (): SyntaxThemePair => ({
    light: resolveVariant(theme, chosen?.light, 'light').syntax,
    dark: resolveVariant(theme, chosen?.dark, 'dark').syntax,
  })
  if (following)
    return pairOf()
  const pair = pairOf()
  // A stray `match-ui` mode cannot reach here from a parsed preference -- the
  // two halves move together -- but this function is total, so it reads the
  // sentinel the way the switch it came from would: follow the app.
  if (pref.mode === MATCH_UI)
    return pair
  const mode = pref.mode === 'system' ? systemMode : pref.mode
  const pinned = mode === 'dark' ? pair.dark : pair.light
  return { light: pinned, dark: pinned }
}

/**
 * The single variant a code surface WEARS -- its palette, not just its tokens.
 *
 * `resolveSyntaxPair` answers which TextMate themes to tokenize with; this
 * answers what the surface those tokens land on must look like. They are two
 * halves of one question, and splitting them is what let a dark syntax theme
 * paint bright tokens onto a light page: Shiki bakes colour into each span, and
 * nothing carried the matching background, so the code sat on the app's
 * surface at a median 1.97:1.
 *
 * Only ONE variant, where the pair has two, because a code surface shows one at
 * a time and Solid re-runs this when the polarity changes. `<html>` needs both
 * halves at once for a different reason -- CSS has to answer an OS flip with no
 * script -- and that reason does not reach here.
 *
 * The mode follows the same three-way choice `resolveSyntaxPair` makes, so the
 * palette and the tokens cannot disagree about which half is showing.
 */
export function resolveSyntaxVariant(
  pref: TerminalThemeValue,
  ui: ThemeValue,
  systemMode: ResolvedThemeMode,
  uiMode: ResolvedThemeMode,
): ThemeVariant {
  const { following, theme, chosen } = resolveThemeSelection(pref, ui)
  // A pinned palette with `mode: 'system'` still follows the OS, exactly as the
  // pair does. `match-ui` in either half means the app answers for both.
  const mode = following || pref.mode === MATCH_UI
    ? uiMode
    : pref.mode === 'system' ? systemMode : pref.mode
  return resolveVariant(theme, chosen?.[mode], mode)
}
