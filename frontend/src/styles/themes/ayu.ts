// Ayu -- adapted from ayu (MIT).
// https://github.com/ayu-theme/ayu-colors
//
// Variants: ayu Light, ayu Mirage and ayu Dark
//
// editor.bg / editor.fg, with ui.bg as the panel surface and
// common.accent.tint as the accent. syntax.comment is stated with an alpha
// upstream; the opaque base colour is used here.
//
// Every comment colour is lifted from upstream, because ayu states them close
// to the 3:1 floor -- Light's #adaeb1 is under it at 2.16:1, Dark's #5a6673
// and Mirage's #6e7c8f only just over at 3.15 and 3.42. Light goes to #787b80
// to clear the floor; the two dark variants go to #99adbf and #aab8ca, which
// is the same 0.81 of their own --foreground's contrast, so a comment reads as
// the same weight on both. Each keeps its upstream hue: ayu's dark variants
// pair a warm foreground with cool blue-grey comments, and that is the
// contrast the eye uses to tell one from the other.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#fcfcfc',
  '--foreground': '#5c6166',
  '--card': '#f8f9fa',
  '--card-foreground': '#5c6166',
  '--primary': '#f29718',
  '--primary-foreground': '#000000',
  '--secondary': '#ececed',
  '--secondary-foreground': '#5c6166',
  '--muted': '#eff0f0',
  '--muted-foreground': '#787b80',
  '--faint': '#f2f3f3',
  '--faint-foreground': '#9d9fa3',
  '--accent': '#eee7dc',
  '--accent-foreground': '#5c6166',
  '--danger': '#e65050',
  '--danger-foreground': '#000000',
  '--success': '#6cbf43',
  '--success-foreground': '#000000',
  '--warning': '#f2a300',
  '--warning-foreground': '#000000',
  '--border': '#dcdedf',
  '--input': '#dbdcde',
  '--ring': '#f29718',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(252, 252, 252, 0.5)',
  '--lm-danger-subtle': '#fbe1e1',
  '--lm-success-subtle': '#eafbe1',
  '--lm-warning-subtle': '#fbf2e1',
  '--lm-icon-monochrome': '#6b6f74',
}

const dark = {
  '--background': '#10141c',
  '--foreground': '#bfbdb6',
  '--card': '#0d1017',
  '--card-foreground': '#bfbdb6',
  '--primary': '#e6b450',
  '--primary-foreground': '#000000',
  '--secondary': '#25282e',
  '--secondary-foreground': '#bfbdb6',
  '--muted': '#22252b',
  '--muted-foreground': '#99adbf',
  '--faint': '#191c24',
  '--faint-foreground': '#738291',
  '--accent': '#3a3325',
  '--accent-foreground': '#bfbdb6',
  '--danger': '#d95757',
  '--danger-foreground': '#000000',
  '--success': '#70bf56',
  '--success-foreground': '#000000',
  '--warning': '#ffb454',
  '--warning-foreground': '#000000',
  '--border': '#2b3242',
  '--input': '#303339',
  '--ring': '#e6b450',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(16, 20, 28, 0.5)',
  '--lm-danger-subtle': '#2f1919',
  '--lm-success-subtle': '#1f2f19',
  '--lm-warning-subtle': '#2f2519',
  '--lm-icon-monochrome': '#aab4bb',
  '--lm-opencode-inner': '#33363b',
  '--lm-opencode-outer': '#d2d1cc',
}

// The sixteen ANSI colours, from the `Ayu Light` and `Ayu` schemes, which carry ayu's own syntax
// hues verbatim (string, entity, constant, regexp).
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#000000',
  red: '#ea6c6d',
  green: '#6cbf43',
  yellow: '#eca944',
  blue: '#3199e1',
  magenta: '#9e75c7',
  cyan: '#46ba94',
  white: '#bababa',
  brightBlack: '#686868',
  brightRed: '#f07171',
  brightGreen: '#86b300',
  brightYellow: '#f2ae49',
  brightBlue: '#399ee6',
  brightMagenta: '#a37acc',
  brightCyan: '#4cbf99',
  brightWhite: '#d1d1d1',
}

const darkAnsi = {
  black: '#11151c',
  red: '#ea6c73',
  green: '#7fd962',
  yellow: '#f9af4f',
  blue: '#53bdfa',
  magenta: '#cda1fa',
  cyan: '#90e1c6',
  white: '#c7c7c7',
  brightBlack: '#686868',
  brightRed: '#f07178',
  brightGreen: '#aad94c',
  brightYellow: '#ffb454',
  brightBlue: '#59c2ff',
  brightMagenta: '#d2a6ff',
  brightCyan: '#95e6cb',
  brightWhite: '#ffffff',
}

// Ayu Mirage -- the mid-tone dark variant, from ayu-theme/ayu-colors'
// `themes/mirage.yaml`. Derived by the same table as the two above; see
// ./types.ts for the two rules a second variant of one polarity follows.
const mirage = {
  '--background': '#242936',
  '--foreground': '#cccac2',
  '--card': '#1f2430',
  '--card-foreground': '#cccac2',
  '--primary': '#ffcc66',
  '--primary-foreground': '#000000',
  '--secondary': '#383c47',
  '--secondary-foreground': '#cccac2',
  '--muted': '#353a44',
  '--muted-foreground': '#aab8ca',
  '--faint': '#2d313d',
  '--faint-foreground': '#838485',
  '--accent': '#564c37',
  '--accent-foreground': '#cccac2',
  '--danger': '#ff6666',
  '--danger-foreground': '#000000',
  '--success': '#87d96c',
  '--success-foreground': '#000000',
  '--warning': '#ffcd66',
  '--warning-foreground': '#000000',
  '--border': '#3b455c',
  '--input': '#434751',
  '--ring': '#ffcc66',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(36, 41, 54, 0.5)',
  '--lm-danger-subtle': '#4d2929',
  '--lm-success-subtle': '#324d29',
  '--lm-warning-subtle': '#4d4129',
  '--lm-icon-monochrome': '#b9c0c6',
  '--lm-opencode-inner': '#464952',
  '--lm-opencode-outer': '#dedbd1',
}

// The sixteen ANSI colours, from the `Ayu Mirage` scheme in the same
// collection the variants above were read from.
const mirageAnsi = {
  black: '#171b24',
  red: '#ed8274',
  green: '#87d96c',
  yellow: '#facc6e',
  blue: '#6dcbfa',
  magenta: '#dabafa',
  cyan: '#90e1c6',
  white: '#c7c7c7',
  brightBlack: '#686868',
  brightRed: '#f28779',
  brightGreen: '#d5ff80',
  brightYellow: '#ffd173',
  brightBlue: '#73d0ff',
  brightMagenta: '#dfbfff',
  brightCyan: '#95e6cb',
  brightWhite: '#ffffff',
}

export const ayuTheme: ThemeDefinition = {
  id: 'ayu',
  label: 'Ayu',
  variants: [
    {
      id: 'ayu-light',
      label: 'Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'ayu-light',
    },
    {
      id: 'ayu-mirage',
      label: 'Mirage',
      polarity: 'dark',
      palette: mirage,
      terminal: mirageAnsi,
      syntax: 'ayu-mirage',
    },
    {
      id: 'ayu-dark',
      label: 'Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'ayu-dark',
    },
  ],
  defaults: { light: 'ayu-light', dark: 'ayu-dark' },
  variantLabel: 'Variant',
  credit: {
    project: 'ayu',
    url: 'https://github.com/ayu-theme/ayu-colors',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
