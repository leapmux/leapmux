// Everforest -- adapted from Everforest (MIT).
// https://github.com/sainnhe/everforest
//
// Variants: Everforest Light and Everforest Dark, both at medium contrast
//
// bg0 background, bg1 panel, grey1 comments, blue accent. The light
// foreground is moved one small step darker so it clears 4.5:1 on the
// selection surface.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#fdf6e3',
  '--foreground': '#556269',
  '--card': '#f4f0d9',
  '--card-foreground': '#556269',
  '--primary': '#3a94c5',
  '--primary-foreground': '#000000',
  '--secondary': '#ece7d7',
  '--secondary-foreground': '#556269',
  '--muted': '#f0ead9',
  '--muted-foreground': '#879285',
  '--faint': '#f3eddc',
  '--faint-foreground': '#a8ae9f',
  '--accent': '#ccdde6',
  '--accent-foreground': '#556269',
  '--danger': '#f85552',
  '--danger-foreground': '#000000',
  '--success': '#8da101',
  '--success-foreground': '#000000',
  '--warning': '#dfa000',
  '--warning-foreground': '#000000',
  '--border': '#dfdabe',
  '--input': '#dcd8c0',
  '--ring': '#3a94c5',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(253, 246, 227, 0.5)',
  '--lm-danger-subtle': '#f8cdcc',
  '--lm-success-subtle': '#f2f8cc',
  '--lm-warning-subtle': '#f8ebcc',
  '--lm-icon-monochrome': '#707c78',
}

const dark = {
  '--background': '#2d353b',
  '--foreground': '#d9ceb6',
  '--card': '#343f44',
  '--card-foreground': '#d9ceb6',
  '--primary': '#7fbbb3',
  '--primary-foreground': '#000000',
  '--secondary': '#42474a',
  '--secondary-foreground': '#d9ceb6',
  '--muted': '#3e4447',
  '--muted-foreground': '#859289',
  '--faint': '#363d41',
  '--faint-foreground': '#6c7873',
  '--accent': '#3c5f5a',
  '--accent-foreground': '#d9ceb6',
  '--danger': '#e67e80',
  '--danger-foreground': '#000000',
  '--success': '#a7c080',
  '--success-foreground': '#000000',
  '--warning': '#dbbc7f',
  '--warning-foreground': '#000000',
  '--border': '#445056',
  '--input': '#475258',
  '--ring': '#7fbbb3',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(45, 53, 59, 0.5)',
  '--lm-danger-subtle': '#562e2f',
  '--lm-success-subtle': '#46562e',
  '--lm-warning-subtle': '#56482e',
  '--lm-icon-monochrome': '#abad9d',
  '--lm-opencode-inner': '#4f5454',
  '--lm-opencode-outer': '#e4ddcc',
}

// The sixteen ANSI colours, from the `Everforest Light Med` and `Everforest Dark Med` schemes.
// ANSI black in the light scheme is Everforest's own `fg` (#5c6a72) rather
// than the scheme's grey0: at grey0 no achromatic slot reached 4.5:1 and
// plain black text rendered washed out.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#5c6a72',
  red: '#e67e80',
  green: '#9ab373',
  yellow: '#c1a266',
  blue: '#7fbbb3',
  magenta: '#d699b6',
  cyan: '#83c092',
  white: '#b2af9f',
  brightBlack: '#a6b0a0',
  brightRed: '#f85552',
  brightGreen: '#8da101',
  brightYellow: '#dfa000',
  brightBlue: '#3a94c5',
  brightMagenta: '#df69ba',
  brightCyan: '#35a77c',
  brightWhite: '#fffbef',
}

const darkAnsi = {
  black: '#7a8478',
  red: '#e67e80',
  green: '#a7c080',
  yellow: '#dbbc7f',
  blue: '#7fbbb3',
  magenta: '#d699b6',
  cyan: '#83c092',
  white: '#f2efdf',
  brightBlack: '#a6b0a0',
  brightRed: '#f85552',
  brightGreen: '#8da101',
  brightYellow: '#dfa000',
  brightBlue: '#3a94c5',
  brightMagenta: '#df69ba',
  brightCyan: '#35a77c',
  brightWhite: '#fffbef',
}

export const everforestTheme: ThemeDefinition = {
  id: 'everforest',
  label: 'Everforest',
  variants: [
    {
      id: 'everforest-light',
      label: 'Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'everforest-light',
    },
    {
      id: 'everforest-dark',
      label: 'Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'everforest-dark',
    },
  ],
  defaults: { light: 'everforest-light', dark: 'everforest-dark' },
  credit: {
    project: 'Everforest',
    url: 'https://github.com/sainnhe/everforest',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
