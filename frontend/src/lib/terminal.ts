import type { ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'
import { loadBrowserPrefs } from './browserStorage'

const DEFAULT_MONO_FONT_FAMILY = '"Hack NF", Hack, "SF Mono", Consolas, monospace'

export type TerminalThemePreference = 'light' | 'dark' | 'match-ui'

export interface TerminalFontOptions {
  fontFamily?: string
  fontSize?: number
  cols?: number
  rows?: number
}

export interface TerminalInstance {
  terminal: Terminal
  fitAddon: FitAddon
  /** When true, onData responses are suppressed (e.g. during snapshot replay). */
  suppressInput: boolean
  /** Send raw input data to the PTY backing this terminal. */
  sendInput?: (data: Uint8Array) => void
  dispose: () => void
}

const DEFAULT_FONT_SIZE = 13

export const darkTerminalTheme: ITheme = {
  background: '#1a1917', // --background
  foreground: '#e8e6e1', // --foreground
  cursor: '#14b8a6', // --primary
  selectionBackground: '#2d3e32', // --accent
  // Dimidium color scheme (https://github.com/dofuuz/dimidium)
  black: '#000000',
  red: '#cf494c',
  green: '#60b442',
  yellow: '#db9c11',
  blue: '#0575d8',
  magenta: '#af5ed2',
  cyan: '#1db6bb',
  white: '#bab7b6',
  brightBlack: '#817e7e',
  brightRed: '#ff643b',
  brightGreen: '#37e57b',
  brightYellow: '#fccd1a',
  brightBlue: '#688dfd',
  brightMagenta: '#ed6fe9',
  brightCyan: '#32e0fb',
  brightWhite: '#dee3e4',
}

export const lightTerminalTheme: ITheme = {
  background: '#fdfcfa', // --background
  foreground: '#22201e', // --foreground
  cursor: '#0d9488', // --primary
  selectionBackground: '#deebe1', // --accent
  // Dimidium Light color scheme (https://github.com/dofuuz/dimidium)
  black: '#000000',
  red: '#b83d41',
  green: '#4d9833',
  yellow: '#ba8300',
  blue: '#0464ba',
  magenta: '#9c50bd',
  cyan: '#019a9f',
  white: '#9c9998',
  brightBlack: '#737575',
  brightRed: '#e0532e',
  brightGreen: '#1fbd62',
  brightYellow: '#d0a803',
  brightBlue: '#4a74ed',
  brightMagenta: '#d05dce',
  brightCyan: '#19b8d0',
  brightWhite: '#b8bdbe',
}

/** Get the stored terminal theme preference from localStorage. */
export function getTerminalThemePreference(): TerminalThemePreference {
  const stored = loadBrowserPrefs().terminalTheme
  if (stored === 'light' || stored === 'dark' || stored === 'match-ui')
    return stored
  return 'match-ui'
}

/** Resolve the terminal theme preference to 'dark' or 'light'. */
export function resolveTerminalThemeMode(pref: TerminalThemePreference): 'dark' | 'light' {
  if (pref === 'light')
    return 'light'
  if (pref === 'dark')
    return 'dark'
  // match-ui: check the current UI theme
  const uiTheme = loadBrowserPrefs().theme || 'system'
  if (uiTheme === 'light')
    return 'light'
  if (uiTheme === 'system') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches
      ? 'dark'
      : 'light'
  }
  return 'dark'
}

/** Resolve the effective terminal theme based on the preference. */
export function resolveTerminalTheme(pref: TerminalThemePreference): ITheme {
  return resolveTerminalThemeMode(pref) === 'dark' ? darkTerminalTheme : lightTerminalTheme
}

/**
 * Apply a TerminalData event (or an initial snapshot from ListTerminals)
 * to an xterm instance. Returns the new resume cursor — callers store
 * this as the tab's `lastOffset` and echo it on the next resubscribe.
 *
 * - isSnapshot=true: backend is replacing the terminal's visible state
 *   (client's after_offset was stale or outside the retained ring). The
 *   xterm buffer is reset before the payload is written so the new bytes
 *   don't append to stale content. The returned cursor is `endOffset`
 *   unconditionally — even if smaller than `currentOffset`, since the
 *   buffer now exactly reflects those `endOffset` bytes.
 * - isSnapshot=false: payload is a strict incremental delta since
 *   `currentOffset`; written without resetting. The returned cursor is
 *   `max(currentOffset, endOffset)` so out-of-order duplicates can't
 *   rewind it.
 */
export function applyTerminalData(
  instance: TerminalInstance,
  data: Uint8Array,
  isSnapshot: boolean,
  endOffset: number,
  currentOffset: number,
  onParsed?: () => void,
): number {
  if (isSnapshot) {
    instance.terminal.reset()
    instance.suppressInput = true
    instance.terminal.write(data, () => {
      instance.suppressInput = false
      onParsed?.()
    })
    return endOffset
  }
  instance.terminal.write(data, onParsed)
  return endOffset > currentOffset ? endOffset : currentOffset
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
  const theme = opts?.theme ?? resolveTerminalTheme(getTerminalThemePreference())

  const terminal = new Terminal({
    cursorBlink: true,
    fontSize: opts?.fontSize || DEFAULT_FONT_SIZE,
    fontFamily: opts?.fontFamily || DEFAULT_MONO_FONT_FAMILY,
    theme,
    ...(opts?.cols ? { cols: opts.cols } : {}),
    ...(opts?.rows ? { rows: opts.rows } : {}),
  })

  const fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)

  try {
    const webglAddon = new WebglAddon()
    terminal.loadAddon(webglAddon)
    webglAddon.onContextLoss(() => {
      webglAddon.dispose()
    })
  }
  catch {
    // WebGL not supported, fall back to canvas renderer
  }

  return {
    terminal,
    fitAddon,
    suppressInput: false,
    dispose() {
      terminal.dispose()
    },
  }
}
