// Tokyo Night -- adapted from Tokyo Night (MIT).
// https://github.com/tokyo-night/tokyo-night-vscode-theme
//
// Variants: Tokyo Night Day (light) and Tokyo Night (dark)
//
// Read from the two VS Code theme documents: editor.background,
// editor.foreground, sideBar.background and the terminal.ansi* entries.
//
// Both documents state a green and a teal, and --success takes the green:
// the colour each highlights the `string` scope with, #9ece6a on Night and
// #385f0d on Day. The teal (#73daca / #33635c) marks object-literal keys and
// links. Taking it on one variant only would leave the theme's two halves
// answering the same question 82 degrees apart -- which is what Day did.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#e6e7ed',
  '--foreground': '#343b59',
  '--card': '#d6d8df',
  '--card-foreground': '#343b59',
  '--primary': '#2959aa',
  '--primary-foreground': '#ffffff',
  '--secondary': '#d4d6de',
  '--secondary-foreground': '#343b59',
  '--muted': '#d8d9e1',
  '--muted-foreground': '#81838c',
  '--faint': '#dbdde4',
  '--faint-foreground': '#9d9fa7',
  '--accent': '#c3cfe2',
  '--accent-foreground': '#343b59',
  '--danger': '#8c4351',
  '--danger-foreground': '#ffffff',
  '--success': '#385f0d',
  '--success-foreground': '#ffffff',
  '--warning': '#8f5e15',
  '--warning-foreground': '#ffffff',
  '--border': '#c1c2c7',
  '--input': '#bec0cd',
  '--ring': '#2959aa',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(230, 231, 237, 0.5)',
  '--lm-danger-subtle': '#f6c1cb',
  '--lm-success-subtle': '#ddf6c1',
  '--lm-warning-subtle': '#f6e1c1',
  '--lm-icon-monochrome': '#5e6375',
}

const dark = {
  '--background': '#1a1b26',
  '--foreground': '#c0caf5',
  '--card': '#16161e',
  '--card-foreground': '#c0caf5',
  '--primary': '#7aa2f7',
  '--primary-foreground': '#000000',
  '--secondary': '#2e303f',
  '--secondary-foreground': '#c0caf5',
  '--muted': '#2b2c3b',
  '--muted-foreground': '#5d658e',
  '--faint': '#222430',
  '--faint-foreground': '#4a5071',
  '--accent': '#2d3546',
  '--accent-foreground': '#c0caf5',
  '--danger': '#f7768e',
  '--danger-foreground': '#000000',
  '--success': '#9ece6a',
  '--success-foreground': '#000000',
  '--warning': '#e0af68',
  '--warning-foreground': '#000000',
  '--border': '#333952',
  '--input': '#3b4261',
  '--ring': '#7aa2f7',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(26, 27, 38, 0.5)',
  '--lm-danger-subtle': '#3c2025',
  '--lm-success-subtle': '#2f3c20',
  '--lm-warning-subtle': '#3c3120',
  '--lm-icon-monochrome': '#8a92bc',
  '--lm-opencode-inner': '#3b3e4f',
  '--lm-opencode-outer': '#d3daf8',
}

// The sixteen ANSI colours, from the `TokyoNight Day` and `TokyoNight Night` schemes. Day uses
// the inverted light mapping.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#e9e9ed',
  red: '#f52a65',
  green: '#587539',
  yellow: '#8c6c3e',
  blue: '#2e7de9',
  magenta: '#9854f1',
  cyan: '#007197',
  white: '#6172b0',
  brightBlack: '#a1a6c5',
  brightRed: '#f52a65',
  brightGreen: '#587539',
  brightYellow: '#8c6c3e',
  brightBlue: '#2e7de9',
  brightMagenta: '#9854f1',
  brightCyan: '#007197',
  brightWhite: '#3760bf',
}

const darkAnsi = {
  black: '#15161e',
  red: '#f7768e',
  green: '#9ece6a',
  yellow: '#e0af68',
  blue: '#7aa2f7',
  magenta: '#bb9af7',
  cyan: '#7dcfff',
  white: '#a9b1d6',
  brightBlack: '#414868',
  brightRed: '#f7768e',
  brightGreen: '#9ece6a',
  brightYellow: '#e0af68',
  brightBlue: '#7aa2f7',
  brightMagenta: '#bb9af7',
  brightCyan: '#7dcfff',
  brightWhite: '#c0caf5',
}

export const tokyoNightTheme: ThemeDefinition = {
  id: 'tokyo-night',
  label: 'Tokyo Night',
  variants: [
    {
      id: 'tokyo-night-day',
      label: 'Day',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'tokyo-night-day',
    },
    {
      id: 'tokyo-night-night',
      label: 'Night',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'tokyo-night',
    },
  ],
  defaults: { light: 'tokyo-night-day', dark: 'tokyo-night-night' },
  credit: {
    project: 'Tokyo Night',
    url: 'https://github.com/tokyo-night/tokyo-night-vscode-theme',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
