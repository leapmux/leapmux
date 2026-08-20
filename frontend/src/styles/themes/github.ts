// GitHub -- adapted from Primer (MIT).
// https://github.com/primer/primitives
//
// Variants: GitHub Light and GitHub Dark
//
// Primer functional tokens: bgColor-default, bgColor-muted, fgColor-default,
// fgColor-muted, borderColor-default, and the accent/danger/success/attention
// foregrounds.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#ffffff',
  '--foreground': '#1f2328',
  '--card': '#f6f8fa',
  '--card-foreground': '#1f2328',
  '--primary': '#0969da',
  '--primary-foreground': '#ffffff',
  '--secondary': '#e9e9ea',
  '--secondary-foreground': '#1f2328',
  '--muted': '#ededee',
  '--muted-foreground': '#59636e',
  '--faint': '#f2f2f2',
  '--faint-foreground': '#878f97',
  '--accent': '#e0e7f0',
  '--accent-foreground': '#1f2328',
  '--danger': '#d1242f',
  '--danger-foreground': '#ffffff',
  '--success': '#1a7f37',
  '--success-foreground': '#ffffff',
  '--warning': '#9a6700',
  '--warning-foreground': '#ffffff',
  '--border': '#d1d9e0',
  '--input': '#d4d5d6',
  '--ring': '#0969da',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(255, 255, 255, 0.5)',
  '--lm-danger-subtle': '#fbe6e8',
  '--lm-success-subtle': '#e6fbec',
  '--lm-warning-subtle': '#fbf5e6',
  '--lm-icon-monochrome': '#3f464e',
}

const dark = {
  '--background': '#0d1117',
  '--foreground': '#f0f6fc',
  '--card': '#151b23',
  '--card-foreground': '#f0f6fc',
  '--primary': '#4493f8',
  '--primary-foreground': '#000000',
  '--secondary': '#282c32',
  '--secondary-foreground': '#f0f6fc',
  '--muted': '#24282e',
  '--muted-foreground': '#9198a1',
  '--faint': '#181c22',
  '--faint-foreground': '#6c727a',
  '--accent': '#222a35',
  '--accent-foreground': '#f0f6fc',
  '--danger': '#f85149',
  '--danger-foreground': '#000000',
  '--success': '#3fb950',
  '--success-foreground': '#000000',
  '--warning': '#d29922',
  '--warning-foreground': '#000000',
  '--border': '#3d444d',
  '--input': '#40454c',
  '--ring': '#4493f8',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(13, 17, 23, 0.5)',
  '--lm-danger-subtle': '#2a1716',
  '--lm-success-subtle': '#162a19',
  '--lm-warning-subtle': '#2a2316',
  '--lm-icon-monochrome': '#bcc2ca',
  '--lm-opencode-inner': '#3a3f45',
  '--lm-opencode-outer': '#f4f9fd',
}

// The sixteen ANSI colours, from the `GitHub Light Default` and `GitHub Dark Default` schemes.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#24292f',
  red: '#cf222e',
  green: '#116329',
  yellow: '#4d2d00',
  blue: '#0969da',
  magenta: '#8250df',
  cyan: '#1b7c83',
  white: '#6e7781',
  brightBlack: '#57606a',
  brightRed: '#a40e26',
  brightGreen: '#1a7f37',
  brightYellow: '#633c01',
  brightBlue: '#218bff',
  brightMagenta: '#a475f9',
  brightCyan: '#3192aa',
  brightWhite: '#8c959f',
}

const darkAnsi = {
  black: '#484f58',
  red: '#ff7b72',
  green: '#3fb950',
  yellow: '#d29922',
  blue: '#58a6ff',
  magenta: '#bc8cff',
  cyan: '#39c5cf',
  white: '#b1bac4',
  brightBlack: '#6e7681',
  brightRed: '#ffa198',
  brightGreen: '#56d364',
  brightYellow: '#e3b341',
  brightBlue: '#79c0ff',
  brightMagenta: '#d2a8ff',
  brightCyan: '#56d4dd',
  brightWhite: '#ffffff',
}

export const githubTheme: ThemeDefinition = {
  id: 'github',
  label: 'GitHub',
  variants: [
    {
      id: 'github-light',
      label: 'Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'github-light',
    },
    {
      id: 'github-dark',
      label: 'Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'github-dark',
    },
  ],
  defaults: { light: 'github-light', dark: 'github-dark' },
  credit: {
    project: 'Primer',
    url: 'https://github.com/primer/primitives',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
