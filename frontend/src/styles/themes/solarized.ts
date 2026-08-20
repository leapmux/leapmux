// Solarized -- adapted from Solarized (MIT).
// https://github.com/altercation/solarized
//
// Variants: Solarized Light and Solarized Dark
//
// Solarized sets body text at base00 (light) and base0 (dark), which are
// deliberately low contrast and do not clear 4.5:1 against this app's panel
// and selection surfaces. Both are moved one small step away from their
// background so they clear it; the accent ramp is untouched.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#fdf6e3',
  '--foreground': '#506167',
  '--card': '#eee8d5',
  '--card-foreground': '#506167',
  '--primary': '#268bd2',
  '--primary-foreground': '#000000',
  '--secondary': '#ece7d7',
  '--secondary-foreground': '#506167',
  '--muted': '#efead9',
  '--muted-foreground': '#849191',
  '--faint': '#f3eddc',
  '--faint-foreground': '#a6ada8',
  '--accent': '#ccdbe6',
  '--accent-foreground': '#506167',
  '--danger': '#dc322f',
  '--danger-foreground': '#ffffff',
  '--success': '#859900',
  '--success-foreground': '#000000',
  '--warning': '#b58900',
  '--warning-foreground': '#000000',
  '--border': '#e2d8b8',
  '--input': '#dad8c8',
  '--ring': '#268bd2',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(253, 246, 227, 0.5)',
  '--lm-danger-subtle': '#f8cdcc',
  '--lm-success-subtle': '#f2f8cc',
  '--lm-warning-subtle': '#f8edcc',
  '--lm-icon-monochrome': '#6d7b7e',
}

const dark = {
  '--background': '#002b36',
  '--foreground': '#91a0a2',
  '--card': '#073642',
  '--card-foreground': '#91a0a2',
  '--primary': '#268bd2',
  '--primary-foreground': '#000000',
  '--secondary': '#113943',
  '--secondary-foreground': '#91a0a2',
  '--muted': '#0e3741',
  '--muted-foreground': '#5f747b',
  '--faint': '#07313b',
  '--faint-foreground': '#446068',
  '--accent': '#293740',
  '--accent-foreground': '#91a0a2',
  '--danger': '#dc322f',
  '--danger-foreground': '#ffffff',
  '--success': '#859900',
  '--success-foreground': '#000000',
  '--warning': '#b58900',
  '--warning-foreground': '#000000',
  '--border': '#094554',
  '--input': '#1c4650',
  '--ring': '#268bd2',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(0, 43, 54, 0.5)',
  '--lm-danger-subtle': '#351d1d',
  '--lm-success-subtle': '#32351d',
  '--lm-warning-subtle': '#352f1d',
  '--lm-icon-monochrome': '#76888d',
  '--lm-opencode-inner': '#1d424c',
  '--lm-opencode-outer': '#b2bcbe',
}

// The sixteen ANSI colours, from the `iTerm2 Solarized Light` and `iTerm2 Solarized Dark`
// schemes, which encode Solarized's own published ANSI assignment.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#073642',
  red: '#dc322f',
  green: '#859900',
  yellow: '#b58900',
  blue: '#268bd2',
  magenta: '#d33682',
  cyan: '#2aa198',
  white: '#bbb5a2',
  brightBlack: '#002b36',
  brightRed: '#cb4b16',
  brightGreen: '#586e75',
  brightYellow: '#657b83',
  brightBlue: '#839496',
  brightMagenta: '#6c71c4',
  brightCyan: '#93a1a1',
  brightWhite: '#fdf6e3',
}

const darkAnsi = {
  black: '#073642',
  red: '#dc322f',
  green: '#859900',
  yellow: '#b58900',
  blue: '#268bd2',
  magenta: '#d33682',
  cyan: '#2aa198',
  white: '#eee8d5',
  brightBlack: '#335e69',
  brightRed: '#cb4b16',
  brightGreen: '#586e75',
  brightYellow: '#657b83',
  brightBlue: '#839496',
  brightMagenta: '#6c71c4',
  brightCyan: '#93a1a1',
  brightWhite: '#fdf6e3',
}

export const solarizedTheme: ThemeDefinition = {
  id: 'solarized',
  label: 'Solarized',
  variants: [
    {
      id: 'solarized-light',
      label: 'Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'solarized-light',
    },
    {
      id: 'solarized-dark',
      label: 'Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'solarized-dark',
    },
  ],
  defaults: { light: 'solarized-light', dark: 'solarized-dark' },
  credit: {
    project: 'Solarized',
    url: 'https://github.com/altercation/solarized',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
