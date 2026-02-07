import type { ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'

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
  dispose: () => void
}

const DEFAULT_FONT_SIZE = 13

const darkTerminalTheme: ITheme = {
  background: '#0d1117',
  foreground: '#e6edf3',
  cursor: '#58a6ff',
  selectionBackground: '#264f78',
  black: '#0d1117',
  red: '#f85149',
  green: '#3fb950',
  yellow: '#d29922',
  blue: '#58a6ff',
  magenta: '#bc8cff',
  cyan: '#39c5cf',
  white: '#e6edf3',
  brightBlack: '#6e7681',
  brightRed: '#ff7b72',
  brightGreen: '#56d364',
  brightYellow: '#e3b341',
  brightBlue: '#79c0ff',
  brightMagenta: '#d2a8ff',
  brightCyan: '#56d4dd',
  brightWhite: '#f0f6fc',
}

const lightTerminalTheme: ITheme = {
  background: '#ffffff',
  foreground: '#1f2328',
  cursor: '#0969da',
  selectionBackground: '#b6d5f0',
  black: '#24292f',
  red: '#cf222e',
  green: '#1a7f37',
  yellow: '#9a6700',
  blue: '#0969da',
  magenta: '#8250df',
  cyan: '#1b7c83',
  white: '#6e7781',
  brightBlack: '#57606a',
  brightRed: '#a40e26',
  brightGreen: '#2da44e',
  brightYellow: '#bf8700',
  brightBlue: '#218bff',
  brightMagenta: '#a475f9',
  brightCyan: '#3192aa',
  brightWhite: '#8c959f',
}

/** Get the stored terminal theme preference from localStorage. */
export function getTerminalThemePreference(): TerminalThemePreference {
  const stored = localStorage.getItem('leapmux-terminal-theme')
  if (stored === 'light' || stored === 'dark' || stored === 'match-ui')
    return stored
  return 'match-ui'
}

/** Resolve the effective terminal theme based on the preference. */
export function resolveTerminalTheme(pref: TerminalThemePreference): ITheme {
  if (pref === 'light')
    return lightTerminalTheme
  if (pref === 'dark')
    return darkTerminalTheme
  // match-ui: check the current UI theme
  // The sentinel 'account-default' means "use the account default" which defaults to 'system'.
  const raw = localStorage.getItem('leapmux-theme')
  const uiTheme = (!raw || raw === 'account-default') ? 'system' : raw
  if (uiTheme === 'light')
    return lightTerminalTheme
  if (uiTheme === 'system') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches
      ? darkTerminalTheme
      : lightTerminalTheme
  }
  return darkTerminalTheme
}

export function createTerminalInstance(opts?: TerminalFontOptions): TerminalInstance {
  const termThemePref = getTerminalThemePreference()
  const theme = resolveTerminalTheme(termThemePref)

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
    dispose() {
      terminal.dispose()
    },
  }
}
