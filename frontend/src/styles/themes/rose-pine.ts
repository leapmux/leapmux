// Rosé Pine -- adapted from Rosé Pine (MIT).
// https://github.com/rose-pine/palette
//
// Variants: Dawn (light), and Moon and Rosé Pine (dark)
//
// `pine` is the light accent and `foam` the dark one, which is how the
// upstream editor themes weight them. `love` is red, `gold` is yellow, and
// whichever of pine/foam is not the accent carries green -- the palette has
// no green of its own.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#faf4ed',
  '--foreground': '#575279',
  '--card': '#fffaf3',
  '--card-foreground': '#575279',
  '--primary': '#286983',
  '--primary-foreground': '#ffffff',
  '--secondary': '#eae4e1',
  '--secondary-foreground': '#575279',
  '--muted': '#ede7e4',
  '--muted-foreground': '#8f8a9b',
  '--faint': '#f0eae6',
  '--faint-foreground': '#ada8b2',
  '--accent': '#d0e2e9',
  '--accent-foreground': '#575279',
  '--danger': '#b4637a',
  '--danger-foreground': '#000000',
  '--success': '#56949f',
  '--success-foreground': '#000000',
  '--warning': '#ea9d34',
  '--warning-foreground': '#000000',
  '--border': '#dad5d3',
  '--input': '#cecacd',
  '--ring': '#286983',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(250, 244, 237, 0.5)',
  '--lm-danger-subtle': '#f9d2dd',
  '--lm-success-subtle': '#d2f3f9',
  '--lm-warning-subtle': '#f9e8d2',
  '--lm-icon-monochrome': '#76718c',
}

const dark = {
  '--background': '#191724',
  '--foreground': '#e0def4',
  '--card': '#1f1d2e',
  '--card-foreground': '#e0def4',
  '--primary': '#9ccfd8',
  '--primary-foreground': '#000000',
  '--secondary': '#312f3d',
  '--secondary-foreground': '#e0def4',
  '--muted': '#2d2b39',
  '--muted-foreground': '#6e6a86',
  '--faint': '#23212e',
  '--faint-foreground': '#56536b',
  '--accent': '#2b3f43',
  '--accent-foreground': '#e0def4',
  '--danger': '#eb6f92',
  '--danger-foreground': '#000000',
  '--success': '#31748f',
  '--success-foreground': '#ffffff',
  '--warning': '#f6c177',
  '--warning-foreground': '#000000',
  '--border': '#403d52',
  '--input': '#524f67',
  '--ring': '#9ccfd8',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(25, 23, 36, 0.5)',
  '--lm-danger-subtle': '#391e26',
  '--lm-success-subtle': '#1e3139',
  '--lm-warning-subtle': '#392e1e',
  '--lm-icon-monochrome': '#a19eb8',
  '--lm-opencode-inner': '#413f4e',
  '--lm-opencode-outer': '#e9e8f7',
}

// The sixteen ANSI colours, from the `Rose Pine Dawn` and `Rose Pine` schemes. Dawn uses Rosé
// Pine's inverted light mapping.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#f2e9e1',
  red: '#b4637a',
  green: '#286983',
  yellow: '#ea9d34',
  blue: '#56949f',
  magenta: '#907aa9',
  cyan: '#d7827e',
  white: '#575279',
  brightBlack: '#9893a5',
  brightRed: '#b4637a',
  brightGreen: '#286983',
  brightYellow: '#ea9d34',
  brightBlue: '#56949f',
  brightMagenta: '#907aa9',
  brightCyan: '#d7827e',
  brightWhite: '#575279',
}

const darkAnsi = {
  black: '#26233a',
  red: '#eb6f92',
  green: '#31748f',
  yellow: '#f6c177',
  blue: '#9ccfd8',
  magenta: '#c4a7e7',
  cyan: '#ebbcba',
  white: '#e0def4',
  brightBlack: '#6e6a86',
  brightRed: '#eb6f92',
  brightGreen: '#31748f',
  brightYellow: '#f6c177',
  brightBlue: '#9ccfd8',
  brightMagenta: '#c4a7e7',
  brightCyan: '#ebbcba',
  brightWhite: '#e0def4',
}

// Rosé Pine Moon -- the softer of the two dark variants, keyed `moon` in
// rose-pine/palette. Its --muted-foreground is lightened from the upstream
// `muted` (#6e6a86 -> #706c88) to clear the 3:1 floor on Moon's background.
const moon = {
  '--background': '#232136',
  '--foreground': '#e0def4',
  '--card': '#2a273f',
  '--card-foreground': '#e0def4',
  '--primary': '#9ccfd8',
  '--primary-foreground': '#000000',
  '--secondary': '#3a384d',
  '--secondary-foreground': '#e0def4',
  '--muted': '#363449',
  '--muted-foreground': '#706c88',
  '--faint': '#2c2a40',
  '--faint-foreground': '#5d5b70',
  '--accent': '#365054',
  '--accent-foreground': '#e0def4',
  '--danger': '#eb6f92',
  '--danger-foreground': '#000000',
  '--success': '#3e8fb0',
  '--success-foreground': '#000000',
  '--warning': '#f6c177',
  '--warning-foreground': '#000000',
  '--border': '#48465b',
  '--input': '#56526e',
  '--ring': '#9ccfd8',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(35, 33, 54, 0.5)',
  '--lm-danger-subtle': '#4b2832',
  '--lm-success-subtle': '#28414b',
  '--lm-warning-subtle': '#4b3c28',
  '--lm-icon-monochrome': '#a4a2b8',
  '--lm-opencode-inner': '#49475c',
  '--lm-opencode-outer': '#e9e7fd',
}

// The sixteen ANSI colours, from the `Rose Pine Moon` scheme in the same
// collection the variants above were read from.
const moonAnsi = {
  black: '#393552',
  red: '#eb6f92',
  green: '#3e8fb0',
  yellow: '#f6c177',
  blue: '#9ccfd8',
  magenta: '#c4a7e7',
  cyan: '#ea9a97',
  white: '#e0def4',
  brightBlack: '#6e6a86',
  brightRed: '#eb6f92',
  brightGreen: '#3e8fb0',
  brightYellow: '#f6c177',
  brightBlue: '#9ccfd8',
  brightMagenta: '#c4a7e7',
  brightCyan: '#ea9a97',
  brightWhite: '#e0def4',
}

export const rosePineTheme: ThemeDefinition = {
  id: 'rose-pine',
  label: 'Rosé Pine',
  variants: [
    {
      id: 'rose-pine-dawn',
      label: 'Dawn',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'rose-pine-dawn',
    },
    {
      id: 'rose-pine-moon',
      label: 'Moon',
      polarity: 'dark',
      palette: moon,
      terminal: moonAnsi,
      syntax: 'rose-pine-moon',
    },
    {
      id: 'rose-pine-main',
      label: 'Rosé Pine',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'rose-pine',
    },
  ],
  defaults: { light: 'rose-pine-dawn', dark: 'rose-pine-main' },
  variantLabel: 'Variant',
  credit: {
    project: 'Rosé Pine',
    url: 'https://github.com/rose-pine/palette',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
