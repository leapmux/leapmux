import type { ITheme } from '@xterm/xterm'
import type { BrowserPreferences, TerminalRendererPreference } from './browserStorage'
import type { TerminalImeHandle } from './terminalIme'
import type {
  ResolvedThemeMode,
  TerminalThemeMode,
  TerminalThemeValue,
  ThemeMode,
  ThemeValue,
} from '~/styles/themes'
import { FitAddon } from '@xterm/addon-fit'
import { SerializeAddon } from '@xterm/addon-serialize'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'
import { DEFAULT_THEME_ID, MATCH_UI, paletteColorToHex, resolveThemeSelection, resolveVariant, themeById } from '~/styles/themes'
import { loadBrowserPrefs } from './browserStorage'
import { copyTextToClipboard } from './clipboard'
import { DEFAULT_MONO_FONT_FAMILY } from './fontStack'
import { createLogger } from './logger'
import { sleep } from './sleep'
import { DEFAULT_TERMINAL_THEME_VALUE, DEFAULT_THEME_VALUE, parseTerminalThemeValue, parseThemeValue } from './themeStore'

const log = createLogger('terminal')
// TextEncoder construction is not free, so one stateless instance serves
// every call site in this module. (Same pattern as ~/lib/crdt/hlc.)
const utf8Encoder = new TextEncoder()

// Default PTY dimensions used by OpenTerminal / RestartTerminal callers
// when no measured size is available yet (new-terminal dialog before
// xterm mount, or a restart on a tab whose ResizeObserver hasn't
// reported dims). Kept in this module so the openTerminal call sites
// and the restart fallback share a single source of truth.
export const DEFAULT_TERMINAL_COLS = 80
export const DEFAULT_TERMINAL_ROWS = 25

// The bytes a terminal puts on the wire for Enter and for erase-one-
// character, matching what xterm itself sends for those keys. Kept beside
// the dimension constants for the same reason: every module that speaks
// the PTY's input dialect — the restart-on-Enter gate in
// useTerminalOperations, the IME replacement path in terminalIme —
// shares one source of truth instead of redefining the byte it needs.
export const ENTER_KEY_CR = 0x0D
export const ERASE_CHARACTER = '\x7F'

type ResolvedTerminalRenderer = 'webgl' | 'canvas'

export interface TerminalFontOptions {
  fontFamily?: string
  fontSize?: number
  cols?: number
  rows?: number
}

export interface TerminalInstance {
  terminal: Terminal
  fitAddon: FitAddon
  /**
   * Serializes the current xterm buffer (visible viewport + scrollback)
   * into an escape-sequence stream that, when written to a freshly reset
   * Terminal, reproduces the same visible state. Used to capture live
   * terminal content on workspace switch-away so the post-initial-snapshot
   * output the user has been watching can be restored on switch-back —
   * `tab.screen` only carries the initial bytes from `ListTerminals` and
   * is otherwise never refreshed with live data.
   */
  serializeAddon: SerializeAddon
  /** When true, onData responses are suppressed (e.g. during snapshot replay). */
  suppressInput: boolean
  /**
   * Whether this terminal is allowed to use the WebGL renderer at all
   * (renderer preference resolves to 'webgl'). When false the terminal stays
   * on the DOM renderer for its whole life and the pool never grants it a
   * context.
   */
  webglAllowed: boolean
  /**
   * Resolves once every (weight, style) variant of the current font family
   * has loaded. The WebGL renderer is only ever attached after this settles,
   * so its glyph atlas is always rasterized with the real font -- never a
   * fallback glyph. Re-assigned by refreshTerminalFont on a font-family swap.
   */
  fontsReady: Promise<void>
  /**
   * The live WebGL addon while this terminal holds a pooled context, else
   * undefined (DOM renderer). Managed exclusively by attachWebgl/detachWebgl
   * driven by the WebGL terminal pool.
   */
  webglAddon?: WebglAddon
  /** Send raw input data to the PTY backing this terminal. */
  sendInput?: (data: Uint8Array) => void
  /**
   * The IME layer, attached once the terminal is open (it needs
   * `terminal.element`). Owns every composed keystroke; see `./terminalIme`
   * for why xterm's own composition handling cannot be left in charge. The
   * VIEW owns both ends of this field's lifecycle: `TerminalView` attaches it
   * after open() and releases it in `disposeTerminalInstance` — this module
   * never touches it, so `dispose()` below does not release it.
   */
  ime?: TerminalImeHandle
  /**
   * The highest byte offset whose write xterm has PARSED (its write callback
   * fired). Undefined while nothing has parsed, and from a snapshot's reset
   * until the snapshot's write completes. The dispose path pairs this with
   * the serialized screen so the stored screen and the tab's `lastOffset`
   * describe the same bytes; the live cursor in tab metadata may run ahead of
   * this, because a write is queued before it parses.
   */
  lastParsedOffset?: number
  dispose: () => void
}

/**
 * Serialize the current xterm buffer (active viewport + full scrollback)
 * into a UTF-8 byte stream of ANSI escape sequences. Writing the result
 * into a freshly `terminal.reset()`-ed instance via `applyTerminalData`
 * with `isSnapshot=true` reproduces the same visible state — that's the
 * exact path used when a terminal's pane is re-created, so the bytes can be
 * stored straight into the tab's `screen` metadata and replayed from there.
 */
export function serializeXtermBuffer(instance: TerminalInstance): Uint8Array {
  const encoded = utf8Encoder.encode(instance.serializeAddon.serialize())
  // Node's TextEncoder (used by jsdom/vitest) returns a Buffer — a
  // Uint8Array subclass — whose prototype chain doesn't pass
  // `instanceof Uint8Array` across realms. Wrap as a plain view over the
  // same bytes so consumers see a predictable type without copying.
  return new Uint8Array(encoded.buffer, encoded.byteOffset, encoded.byteLength)
}

export const DEFAULT_FONT_SIZE = 13

/** Get the stored terminal theme preference from localStorage. */
export function getTerminalThemePreference(prefs: BrowserPreferences = loadBrowserPrefs()): TerminalThemeValue {
  return parseTerminalThemeValue(prefs.terminalTheme) ?? DEFAULT_TERMINAL_THEME_VALUE
}

/** Get the stored terminal renderer preference from localStorage. */
export function getTerminalRendererPreference(prefs: BrowserPreferences = loadBrowserPrefs()): TerminalRendererPreference {
  const stored = prefs.terminalRenderer
  if (stored === 'auto' || stored === 'webgl' || stored === 'canvas')
    return stored
  return 'auto'
}

/** Resolve the renderer preference, defaulting Linux desktop Tauri away from WebGL. */
export function resolveTerminalRendererPreference(
  pref: TerminalRendererPreference,
): ResolvedTerminalRenderer {
  if (pref === 'canvas' || pref === 'webgl')
    return pref
  return isLinuxDesktopTauri() ? 'canvas' : 'webgl'
}

function isLinuxDesktopTauri(): boolean {
  if (typeof window === 'undefined' || typeof navigator === 'undefined')
    return false
  if (typeof window.__TAURI_INTERNALS__ === 'undefined')
    return false
  // Android UA also contains "Linux", so exclude mobile explicitly.
  return /\bLinux\b/i.test(navigator.userAgent) && !/android|mobile/i.test(navigator.userAgent)
}

/**
 * Resolve the terminal's MODE to 'dark' or 'light'.
 *
 * `match-ui` means the whole preference follows the app, so the answer is the
 * UI's own mode. `TerminalThemeValue` holds the two halves to one decision, so
 * this sentinel and the palette's arrive together and never on their own. A
 * pinned mode answers for itself, and `system` reads the OS -- the same thing
 * it means in the Theme row.
 *
 * `uiMode` and `prefersDark` are passed in explicitly so callers in a reactive
 * context can wire them to signals — reading them inside the function would
 * bypass Solid's dependency tracking and the terminal would stop following the
 * UI theme after the first resolution.
 */
export function resolveTerminalThemeMode(
  pref: TerminalThemeMode,
  uiMode: ThemeMode,
  prefersDark: boolean,
): ResolvedThemeMode {
  const mode = pref === MATCH_UI ? uiMode : pref
  if (mode === 'light' || mode === 'dark')
    return mode
  return prefersDark ? 'dark' : 'light'
}

/**
 * The xterm theme for a resolved (palette, mode) pair.
 *
 * MEMOIZED, and that is load-bearing rather than an optimisation. `TerminalView`
 * guards its palette-rebuild effect with `theme === lastTheme`, which worked
 * when this returned one of two module constants. Building a fresh object per
 * call would make that guard never fire, and every reactive tick would rewrite
 * `options.theme` on every xterm instance in every tile -- each write recomputes
 * the whole colour table.
 *
 * The sixteen ANSI colours come from the theme's own `terminal` set; the
 * background, foreground, cursor and selection come from that same theme's UI
 * palette, so the terminal chrome always agrees with the palette it claims to
 * be. A theme this build does not carry falls back to Default, the same way
 * `themeById` answers everywhere else.
 */
const terminalThemeCache = new Map<string, ITheme>()

export function terminalThemeFor(name: string, mode: ResolvedThemeMode, variant?: string): ITheme {
  const theme = themeById(name)
  const resolved = resolveVariant(theme, variant, mode)
  // Keyed on the RESOLVED VARIANT id, which is already globally unique and
  // already carries the polarity. Two unknown names both fall back to Default
  // and share one entry rather than growing the cache, exactly as before.
  const cached = terminalThemeCache.get(resolved.id)
  if (cached)
    return cached
  // Normalized to hex on the way out of CSS. xterm parses a hex literal or a
  // COMMA-separated `rgb()`; Default states these four as space-separated CSS
  // Color 4, which xterm cannot read, so it fell through to a canvas probe and
  // -- wherever no 2D context is available -- `parseColor` swallowed the throw
  // and painted black on white instead of the theme, with nothing logged.
  const built: ITheme = {
    ...resolved.terminal,
    background: paletteColorToHex(resolved.palette['--background']!),
    foreground: paletteColorToHex(resolved.palette['--foreground']!),
    cursor: paletteColorToHex(resolved.palette['--primary']!),
    selectionBackground: paletteColorToHex(resolved.palette['--accent']!),
  }
  terminalThemeCache.set(resolved.id, built)
  return built
}

/**
 * The Default theme's terminal, which every theme's terminal is now a sibling
 * of. Named exports because the tests read them, and because "the terminal
 * Default paints" is worth being able to name.
 *
 * The sixteen ANSI colours are Dimidium's; see ~/styles/themes/default.ts.
 *
 * DECLARED AFTER `terminalThemeFor`, not beside the other constants at the top:
 * these initialisers call it, and it closes over `terminalThemeCache`. A `const`
 * is in its temporal dead zone until its own declaration runs, so evaluating
 * these earlier threw `Cannot access 'terminalThemeCache' before initialization`
 * at import time -- which took down every module that transitively imports this
 * one, not just the terminal.
 */
export const darkTerminalTheme: ITheme = terminalThemeFor(DEFAULT_THEME_ID, 'dark')
export const lightTerminalTheme: ITheme = terminalThemeFor(DEFAULT_THEME_ID, 'light')

/**
 * Resolve the effective terminal theme from both preferences.
 *
 * `ui` is the UI preference (palette id + mode), which is what the terminal's
 * `match-ui` sentinel refers to. Both halves carry the sentinel or neither
 * does, so `ui` supplies the whole answer or none of it.
 */
export function resolveTerminalTheme(
  pref: TerminalThemeValue,
  ui: ThemeValue,
  prefersDark: boolean,
): ITheme {
  const mode = resolveTerminalThemeMode(pref.mode, ui.mode, prefersDark)
  // The variant follows whichever preference supplied the palette: a row on the
  // sentinel wears the app's variant, a detached row wears its own. Reading the
  // app's variant under a detached palette would name a variant of the wrong
  // theme, which `resolveVariant` would then discard for the theme's default.
  const { theme, chosen } = resolveThemeSelection(pref, ui)
  return terminalThemeFor(theme.id, mode, chosen?.[mode])
}

/**
 * One terminal-data frame to apply, in the shape the caller knows it by:
 * a snapshot replaces the visible state, a delta extends it. The union
 * states the delta-range contract in the type — a delta's range is
 * [endOffset - data.length, endOffset) — where a boolean flag and two
 * adjacent same-typed offsets left it to a doc comment.
 */
export type TerminalDataPayload
  = | { kind: 'snapshot', data: Uint8Array, endOffset: number, onParsed?: () => void }
    | { kind: 'delta', data: Uint8Array, endOffset: number, currentOffset: number, onParsed?: () => void }

/**
 * Apply a TerminalData event (or an initial snapshot from ListTerminals)
 * to an xterm instance. Returns the new resume cursor — callers store
 * this as the tab's `lastOffset` and echo it on the next resubscribe.
 *
 * - kind=snapshot: backend is replacing the terminal's visible state
 *   (client's after_offset was stale or outside the retained ring). The
 *   xterm buffer is reset before the payload is written so the new bytes
 *   don't append to stale content. The returned cursor is `endOffset`
 *   unconditionally — even if smaller than the caller's current offset,
 *   since the buffer now exactly reflects those `endOffset` bytes.
 * - kind=delta: payload is a delta whose range is
 *   [endOffset - data.length, endOffset). A delta may OVERLAP what this
 *   terminal already rendered: the worker registers a new watcher for live
 *   events first and takes its catch-up snapshot after, so any byte the
 *   live path already delivered also rides in the catch-up range. The
 *   offsets are authoritative on both ends, so the already-rendered prefix
 *   is trimmed here — rendering stays exactly-once no matter how the two
 *   deliveries interleave. A gap (delta starts BEYOND the current offset)
 *   means bytes were missed; nothing here can recover them, so the delta
 *   writes as-is and the next snapshot rebuilds the screen. The returned
 *   cursor is `max(currentOffset, endOffset)` so out-of-order duplicates
 *   can't rewind it.
 */
export function applyTerminalData(instance: TerminalInstance, payload: TerminalDataPayload): number {
  if (payload.kind === 'snapshot') {
    instance.terminal.reset()
    instance.suppressInput = true
    // reset() emptied the buffer, so nothing is parsed until the write below
    // completes — see `lastParsedOffset`.
    instance.lastParsedOffset = undefined
    instance.terminal.write(payload.data, () => {
      instance.lastParsedOffset = payload.endOffset
      instance.suppressInput = false
      payload.onParsed?.()
    })
    return payload.endOffset
  }
  const { data, endOffset, currentOffset, onParsed } = payload
  const deltaStart = endOffset - data.byteLength
  const unrendered = currentOffset > deltaStart ? data.subarray(currentOffset - deltaStart) : data
  if (unrendered.byteLength === 0) {
    onParsed?.()
    return Math.max(currentOffset, endOffset)
  }
  const cursor = Math.max(currentOffset, endOffset)
  instance.terminal.write(unrendered, () => {
    instance.lastParsedOffset = cursor
    onParsed?.()
  })
  return cursor
}

/**
 * Returns true when any line in the active buffer contains at least one
 * non-whitespace character (after trimming trailing spaces, which xterm
 * pads unused cells with). Used to decide when to drop the "Starting
 * terminal…" overlay — the moment the shell has actually painted its
 * prompt, not just the moment the PTY spawned.
 */
export function bufferHasVisibleContent(terminal: Terminal): boolean {
  const buffer = terminal.buffer.active
  for (let i = 0; i < buffer.length; i++) {
    const line = buffer.getLine(i)
    if (!line)
      continue
    if (line.translateToString(true).trim().length > 0)
      return true
  }
  return false
}

export function createTerminalInstance(opts?: TerminalFontOptions & { theme?: ITheme }): TerminalInstance {
  const prefs = loadBrowserPrefs()
  const theme = opts?.theme ?? resolveTerminalTheme(
    getTerminalThemePreference(prefs),
    parseThemeValue(prefs.theme) ?? DEFAULT_THEME_VALUE,
    typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-color-scheme: dark)').matches,
  )
  const fontFamily = opts?.fontFamily || DEFAULT_MONO_FONT_FAMILY
  const fontSize = opts?.fontSize || DEFAULT_FONT_SIZE

  const terminal = new Terminal({
    cursorBlink: true,
    fontSize,
    fontFamily,
    theme,
    ...(opts?.cols ? { cols: opts.cols } : {}),
    ...(opts?.rows ? { rows: opts.rows } : {}),
  })

  const fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)

  // The serialize addon mirrors writes into an internal buffer so we can
  // ask xterm for an escape-sequence dump that reproduces the visible
  // state. Loaded unconditionally — there is no renderer-specific path
  // to gate on, and the per-write overhead is negligible compared to
  // the WebGL atlas work.
  const serializeAddon = new SerializeAddon()
  terminal.loadAddon(serializeAddon)

  // Renderer selection is deferred: terminals start on xterm's DOM renderer
  // and the WebGL terminal pool lazily attaches a bounded number of WebGL
  // contexts to the on-screen terminals that most need one (see
  // webglTerminalPool). `webglAllowed` records whether this terminal may ever
  // receive a context; the DOM renderer is the fallback for everyone else.
  const preference = getTerminalRendererPreference(prefs)
  const webglAllowed = resolveTerminalRendererPreference(preference) === 'webgl'

  // Web fonts (e.g. Hack NF) are declared with font-display: swap and load
  // asynchronously. The WebGL renderer rasterizes glyphs into a texture atlas
  // as cells paint, caching fallback-font glyphs for any (weight, style)
  // variant that hasn't loaded yet. Kick off fetches for every variant now and
  // expose the promise so the pool can hold WebGL attach until the real font
  // is ready — the atlas is then always built from the loaded font.
  //
  // Only terminals that may receive a WebGL context need this gate: the DOM
  // renderer has no atlas and paints via CSS font-family, so a WebGL-ineligible
  // terminal would fetch four variants (and arm their cold-start retry timers)
  // whose readiness `fontsReady` nobody ever awaits. Skip the fan-out for them.
  const fontsReady = webglAllowed
    ? loadTerminalFonts(terminal, fontFamily, fontSize)
    : Promise.resolve()

  // Auto-copy the selection so users don't have to press Cmd/Ctrl+C
  // (mirrors iTerm2's "Copy on Select" behavior). Empty selections
  // (e.g. a click that clears highlight) are skipped so we don't
  // clobber whatever the user has on the clipboard.
  terminal.onSelectionChange(() => {
    void copyTextToClipboard(terminal.getSelection())
  })

  const instance: TerminalInstance = {
    terminal,
    fitAddon,
    serializeAddon,
    suppressInput: false,
    webglAllowed,
    fontsReady,
    webglAddon: undefined,
    dispose() {
      terminal.dispose()
    },
  }
  return instance
}

/**
 * Attach the WebGL renderer to a terminal, wiring context-loss handling.
 *
 * Called only by the WebGL terminal pool, which has already awaited the
 * instance's `fontsReady` and verified the terminal is on-screen and within
 * the context budget. The pool-supplied `onContextLoss` lets the pool free the
 * slot and re-attach elsewhere when the browser force-drops the GPU context.
 *
 * Returns whether the renderer attached. A false return (WebGL unavailable,
 * e.g. jsdom or a lost driver) leaves the terminal on the DOM renderer, which
 * is fully correct.
 */
export function attachWebgl(instance: TerminalInstance, onContextLoss: () => void): boolean {
  if (!instance.webglAllowed)
    return false
  if (instance.webglAddon)
    return true
  try {
    const addon = new WebglAddon()
    instance.terminal.loadAddon(addon)
    addon.onContextLoss(() => {
      log.warn('terminal_renderer_webgl_context_lost')
      // Drop our reference before disposing so a re-entrant detach is a no-op,
      // then notify the pool so it can re-attach if the terminal is still on
      // screen and within budget.
      if (instance.webglAddon === addon)
        instance.webglAddon = undefined
      try {
        addon.dispose()
      }
      catch {
        // Already torn down by the context loss itself.
      }
      onContextLoss()
    })
    instance.webglAddon = addon
    return true
  }
  catch (error) {
    log.warn('terminal_renderer', { renderer: 'canvas', reason: 'webgl_unavailable', error })
    return false
  }
}

/**
 * Detach the WebGL renderer, reverting the terminal to the DOM renderer.
 * Idempotent: a no-op when no WebGL addon is attached.
 */
export function detachWebgl(instance: TerminalInstance): void {
  const addon = instance.webglAddon
  if (!addon)
    return
  instance.webglAddon = undefined
  try {
    addon.dispose()
  }
  catch {
    // Terminal (and its addons) may already have been disposed.
  }
}

// xterm rasterizes a separate glyph for each (weight, style) combination,
// so we have to trigger fetches for all four variants — not just the
// regular face the renderer happens to paint first.
const FONT_VARIANTS: ReadonlyArray<{ weight: string, style: string }> = [
  { weight: 'normal', style: 'normal' },
  { weight: 'bold', style: 'normal' },
  { weight: 'normal', style: 'italic' },
  { weight: 'bold', style: 'italic' },
]

// Cold-start font fetches can fail transiently -- e.g. Tauri's static-file
// server is briefly unavailable right after launch. Retry a failed variant a
// bounded number of times with exponential backoff, then give up so a
// genuinely-missing font doesn't spin forever.
const FONT_LOAD_MAX_RETRIES = 4
const FONT_LOAD_RETRY_BASE_MS = 300

/** Load one font variant, resolving to whether it settled without a fetch error. */
function tryLoadFontVariant(spec: string): Promise<boolean> {
  // A resolve (even with zero matched faces, e.g. a pure system-font family)
  // means "nothing failed"; only a reject signals a matched @font-face whose
  // fetch failed and is worth retrying.
  //
  // `document.fonts.load` throws a SyntaxError *synchronously* when `spec` is
  // not a parseable CSS font shorthand (e.g. a malformed custom family from a
  // corrupted preference). Catch it so a bad spec degrades to a failed variant
  // rather than rejecting `fontsReady` -- which would throw into the pool's
  // attach and, at construction, out of createTerminalInstance itself.
  try {
    return document.fonts.load(spec).then(() => true, () => false)
  }
  catch {
    return Promise.resolve(false)
  }
}

/**
 * Fetch every (weight, style) variant xterm rasterizes into its glyph atlas
 * and keep `terminal`'s atlas honest across cold-start fetch failures.
 *
 * The returned promise (assigned to `fontsReady`) resolves as soon as the
 * FIRST attempt settles -- success or failure -- so the pool's WebGL attach is
 * never blocked on a slow or failing fetch. Kicking off explicit fetches is
 * more reliable than waiting on `document.fonts.ready`: that promise only
 * awaits loads already in flight, and at construction the renderer may not have
 * referenced the font yet, so `.ready` can resolve before any fetch starts.
 *
 * If a variant fails on the first attempt (a matched @font-face rejected), it
 * is retried in the background with backoff; once it finally loads, the glyph
 * atlas is cleared so a WebGL renderer that cached fallback glyphs re-paints
 * with the real font. Without this, a terminal that attached WebGL during the
 * outage would show fallback glyphs until the next font-family change or a
 * reload. `clearTextureAtlas()` is a no-op on the DOM renderer, so the retry
 * clear is harmless for terminals without a WebGL context.
 */
export function loadTerminalFonts(terminal: Terminal, fontFamily: string, fontSize: number): Promise<void> {
  if (typeof document === 'undefined' || !document.fonts?.load)
    return Promise.resolve()
  const specs = FONT_VARIANTS.map(({ weight, style }) => `${style} ${weight} ${fontSize}px ${fontFamily}`)
  return Promise.all(specs.map(tryLoadFontVariant)).then((settled) => {
    const failed = specs.filter((_, i) => !settled[i])
    if (failed.length > 0)
      void retryFontVariants(terminal, failed)
  })
}

/**
 * Retry the given font-variant specs with exponential backoff until they load
 * or the attempt budget is exhausted, clearing `terminal`'s atlas whenever a
 * batch makes progress so an attached WebGL renderer drops its cached fallback
 * glyphs.
 */
async function retryFontVariants(terminal: Terminal, specs: string[]): Promise<void> {
  let pending = specs
  for (let attempt = 0; attempt < FONT_LOAD_MAX_RETRIES && pending.length > 0; attempt++) {
    await sleep(FONT_LOAD_RETRY_BASE_MS * 2 ** attempt)
    // The document (and its FontFaceSet) can go away mid-retry during teardown;
    // stop rather than throw into this floating promise.
    if (typeof document === 'undefined' || !document.fonts?.load)
      return
    const settled = await Promise.all(pending.map(tryLoadFontVariant))
    if (settled.some(Boolean))
      scheduleAtlasClear(terminal)
    pending = pending.filter((_, i) => !settled[i])
  }
}

/**
 * React to a font-family change on a live terminal instance: point xterm at
 * the new family, re-arm `fontsReady` for the new variants, and — once they
 * load — clear the glyph atlas so an attached WebGL renderer re-rasterizes
 * with the new font.
 *
 * Re-arming `fontsReady` is what keeps the atlas correct across a swap: a
 * later (or in-flight) pool attach awaits the *current* `fontsReady`, so it
 * never builds the atlas from the outgoing font.
 *
 * Why a promise-based clear and not a `FontFaceSet` `loadingdone` listener:
 * when the first fetch attempt fails (e.g. Tauri's static-file server is
 * briefly unavailable on cold start) and the retry succeeds, faces
 * transition `error → loaded` and Chromium does not emit `loadingdone`.
 * `document.fonts.load(...)` resolves cleanly in that case.
 *
 * Two clear passes — once after our `load()` promises settle, once on the
 * next animation frame — cover the case where the renderer's first paint
 * rasterized fallback glyphs in the brief window between the load completing
 * and our `.then()` callback running. `clearTextureAtlas()` is a no-op on the
 * DOM renderer, so this is harmless for terminals without a WebGL context.
 *
 * Returns whether the family actually changed, so callers can gate follow-up
 * work (e.g. re-fitting) on a real swap: because every mounted TerminalView
 * iterates the shared `instances` map, the first view to see a swap does the
 * work and the rest get `false` and skip it, avoiding N-views x M-terminals
 * redundant reflows on a single font change.
 */
export function refreshTerminalFont(
  instance: TerminalInstance,
  fontFamily: string,
  fontSize: number,
): boolean {
  // Skip when the family is unchanged. The font-preference effect re-fires
  // whenever the preferences store hydrates -- a fresh `monoFonts` array is a
  // new signal value even when the resolved family string is byte-identical --
  // and re-arming `fontsReady` with a new unsettled promise on every such fire
  // would needlessly delay the pool's WebGL attach (which awaits it) and
  // re-fetch every font variant.
  if (instance.terminal.options.fontFamily === fontFamily)
    return false
  instance.terminal.options.fontFamily = fontFamily
  const ready = loadTerminalFonts(instance.terminal, fontFamily, fontSize)
  instance.fontsReady = ready
  void ready.then(() => scheduleAtlasClear(instance.terminal))
  return true
}

/**
 * Clear the glyph atlas now and once more on the next animation frame. The
 * second pass covers the window where the renderer's first paint rasterized
 * fallback glyphs between a font load completing and this callback running.
 * Shared by the font-swap path and the cold-start retry so both drop stale
 * fallback glyphs identically. A no-op on the DOM renderer (which has no atlas).
 */
function scheduleAtlasClear(terminal: Terminal): void {
  clearAtlas(terminal)
  if (typeof requestAnimationFrame !== 'undefined')
    requestAnimationFrame(() => clearAtlas(terminal))
}

function clearAtlas(terminal: Terminal): void {
  try {
    terminal.clearTextureAtlas()
  }
  catch {
    // Terminal was disposed before fonts settled; nothing to do.
  }
}
