// Catppuccin -- adapted from Catppuccin (MIT).
// https://github.com/catppuccin/palette
//
// Variants: Latte (light), and Frappé, Macchiato and Mocha (dark)
//
// Backgrounds are base/mantle, text is `text`, comments are overlay1, and
// the accent is `blue`. Reds, greens and yellows are the flavour's own.
// Every flavour states its own value for each of those roles, so the four
// differ in more than lightness -- upstream shifts the accents too.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#eff1f5',
  '--foreground': '#4c4f69',
  '--card': '#e6e9ef',
  '--card-foreground': '#4c4f69',
  '--primary': '#1e66f5',
  '--primary-foreground': '#ffffff',
  '--secondary': '#dfe1e7',
  '--secondary-foreground': '#4c4f69',
  '--muted': '#e2e4ea',
  '--muted-foreground': '#86899b',
  '--faint': '#e5e7ed',
  '--faint-foreground': '#a3a6b4',
  '--accent': '#ced7e8',
  '--accent-foreground': '#4c4f69',
  '--danger': '#d20f39',
  '--danger-foreground': '#ffffff',
  '--success': '#40a02b',
  '--success-foreground': '#000000',
  '--warning': '#df8e1d',
  '--warning-foreground': '#000000',
  '--border': '#ccd0da',
  '--input': '#bcc0cc',
  '--ring': '#1e66f5',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(239, 241, 245, 0.5)',
  '--lm-danger-subtle': '#f8d0d8',
  '--lm-success-subtle': '#d7f8d0',
  '--lm-warning-subtle': '#f8e7d0',
  '--lm-icon-monochrome': '#6c6f84',
}

const dark = {
  '--background': '#1e1e2e',
  '--foreground': '#cdd6f4',
  '--card': '#181825',
  '--card-foreground': '#cdd6f4',
  '--primary': '#89b4fa',
  '--primary-foreground': '#000000',
  '--secondary': '#333446',
  '--secondary-foreground': '#cdd6f4',
  '--muted': '#303042',
  '--muted-foreground': '#7f849c',
  '--faint': '#272738',
  '--faint-foreground': '#64677d',
  '--accent': '#323c4d',
  '--accent-foreground': '#cdd6f4',
  '--danger': '#f38ba8',
  '--danger-foreground': '#000000',
  '--success': '#a6e3a1',
  '--success-foreground': '#000000',
  '--warning': '#f9e2af',
  '--warning-foreground': '#000000',
  '--border': '#3a3b50',
  '--input': '#45475a',
  '--ring': '#89b4fa',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(30, 30, 46, 0.5)',
  '--lm-danger-subtle': '#44242d',
  '--lm-success-subtle': '#274424',
  '--lm-warning-subtle': '#443a24',
  '--lm-icon-monochrome': '#a2a9c4',
  '--lm-opencode-inner': '#414356',
  '--lm-opencode-outer': '#dce2f7',
}

// The sixteen ANSI colours, from the `Catppuccin Latte` and `Catppuccin Mocha` schemes. Latte
// follows Catppuccin's own inverted light mapping: ANSI black is a light
// surface and ANSI white is dark text, so a program written for a dark
// terminal still reads correctly.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#bcc0cc',
  red: '#d20f39',
  green: '#40a02b',
  yellow: '#df8e1d',
  blue: '#1e66f5',
  magenta: '#ea76cb',
  cyan: '#179299',
  white: '#5c5f77',
  brightBlack: '#acb0be',
  brightRed: '#e7103f',
  brightGreen: '#46b02f',
  brightYellow: '#e49931',
  brightBlue: '#3878f6',
  brightMagenta: '#ef95d7',
  brightCyan: '#19a1a8',
  brightWhite: '#6c6f85',
}

const darkAnsi = {
  black: '#45475a',
  red: '#f38ba8',
  green: '#a6e3a1',
  yellow: '#f9e2af',
  blue: '#89b4fa',
  magenta: '#f5c2e7',
  cyan: '#94e2d5',
  white: '#bac2de',
  brightBlack: '#585b70',
  brightRed: '#f7aec2',
  brightGreen: '#c2ecbf',
  brightYellow: '#fcd682',
  brightBlue: '#aeccfc',
  brightMagenta: '#f398da',
  brightCyan: '#b1eae1',
  brightWhite: '#a6adc8',
}

// Catppuccin Frappé -- the lowest-contrast of the three dark flavours.
// Upstream marks it `dark: true` in palette.json, order 1 of 4.
const frappe = {
  '--background': '#303446',
  '--foreground': '#c6d0f5',
  '--card': '#292c3c',
  '--card-foreground': '#c6d0f5',
  '--primary': '#8caaee',
  '--primary-foreground': '#000000',
  '--secondary': '#42475b',
  '--secondary-foreground': '#c6d0f5',
  '--muted': '#3f4458',
  '--muted-foreground': '#838ba7',
  '--faint': '#383c4f',
  '--faint-foreground': '#6c728c',
  '--accent': '#424d67',
  '--accent-foreground': '#c6d0f5',
  '--danger': '#e78284',
  '--danger-foreground': '#000000',
  '--success': '#a6d189',
  '--success-foreground': '#000000',
  '--warning': '#e5c890',
  '--warning-foreground': '#000000',
  '--border': '#4a4e65',
  '--input': '#51576d',
  '--ring': '#8caaee',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(48, 52, 70, 0.5)',
  '--lm-danger-subtle': '#5f3334',
  '--lm-success-subtle': '#455f33',
  '--lm-warning-subtle': '#5f5033',
  '--lm-icon-monochrome': '#a1aaca',
  '--lm-opencode-inner': '#4e5369',
  '--lm-opencode-outer': '#d3ddff',
}

// Catppuccin Macchiato -- the middle dark flavour, order 2 of 4.
const macchiato = {
  '--background': '#24273a',
  '--foreground': '#cad3f5',
  '--card': '#1e2030',
  '--card-foreground': '#cad3f5',
  '--primary': '#8aadf4',
  '--primary-foreground': '#000000',
  '--secondary': '#383c50',
  '--secondary-foreground': '#cad3f5',
  '--muted': '#35394d',
  '--muted-foreground': '#8087a2',
  '--faint': '#2d3044',
  '--faint-foreground': '#666c85',
  '--accent': '#394358',
  '--accent-foreground': '#cad3f5',
  '--danger': '#ed8796',
  '--danger-foreground': '#000000',
  '--success': '#a6da95',
  '--success-foreground': '#000000',
  '--warning': '#eed49f',
  '--warning-foreground': '#000000',
  '--border': '#3e435b',
  '--input': '#494d64',
  '--ring': '#8aadf4',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(36, 39, 58, 0.5)',
  '--lm-danger-subtle': '#4f2b30',
  '--lm-success-subtle': '#344f2b',
  '--lm-warning-subtle': '#4f432b',
  '--lm-icon-monochrome': '#a1a9c7',
  '--lm-opencode-inner': '#45495f',
  '--lm-opencode-outer': '#d8e2ff',
}

// The sixteen ANSI colours, from the `Catppuccin Frappe` scheme in the same
// collection the variants above were read from.
const frappeAnsi = {
  black: '#51576d',
  red: '#e78284',
  green: '#a6d189',
  yellow: '#e5c890',
  blue: '#8caaee',
  magenta: '#f4b8e4',
  cyan: '#81c8be',
  white: '#b5bfe2',
  brightBlack: '#626880',
  brightRed: '#eda0a2',
  brightGreen: '#b9dba2',
  brightYellow: '#ecd7ae',
  brightBlue: '#adc2f3',
  brightMagenta: '#f38ed8',
  brightCyan: '#98d2ca',
  brightWhite: '#a5adce',
}

// The sixteen ANSI colours, from the `Catppuccin Macchiato` scheme in the same
// collection the variants above were read from.
const macchiatoAnsi = {
  black: '#494d64',
  red: '#ed8796',
  green: '#a6da95',
  yellow: '#eed49f',
  blue: '#8aadf4',
  magenta: '#f5bde6',
  cyan: '#8bd5ca',
  white: '#b8c0e0',
  brightBlack: '#5b6078',
  brightRed: '#f2a7b2',
  brightGreen: '#bde3b0',
  brightYellow: '#f4e3c1',
  brightBlue: '#adc5f7',
  brightMagenta: '#f493da',
  brightCyan: '#a5ded6',
  brightWhite: '#a5adcb',
}

export const catppuccinTheme: ThemeDefinition = {
  id: 'catppuccin',
  label: 'Catppuccin',
  variants: [
    {
      id: 'catppuccin-latte',
      label: 'Latte',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'catppuccin-latte',
    },
    {
      id: 'catppuccin-frappe',
      label: 'Frappé',
      polarity: 'dark',
      palette: frappe,
      terminal: frappeAnsi,
      syntax: 'catppuccin-frappe',
    },
    {
      id: 'catppuccin-macchiato',
      label: 'Macchiato',
      polarity: 'dark',
      palette: macchiato,
      terminal: macchiatoAnsi,
      syntax: 'catppuccin-macchiato',
    },
    {
      id: 'catppuccin-mocha',
      label: 'Mocha',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'catppuccin-mocha',
    },
  ],
  defaults: { light: 'catppuccin-latte', dark: 'catppuccin-mocha' },
  variantLabel: 'Flavor',
  credit: {
    project: 'Catppuccin',
    url: 'https://github.com/catppuccin/palette',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
