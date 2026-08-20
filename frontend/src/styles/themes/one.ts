// One -- adapted from One (MIT).
// https://github.com/atom/one-dark-syntax
//
// Variants: One Light and One Dark
//
// The upstream palettes are HSL in styles/colors.less; the hex here is the
// direct conversion. hue-2 is the accent, hue-5 red, hue-4 green, hue-6-2
// yellow, and mono-3 the comment colour.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#fafafa',
  '--foreground': '#383a42',
  '--card': '#eaeaeb',
  '--card-foreground': '#383a42',
  '--primary': '#4078f2',
  '--primary-foreground': '#000000',
  '--secondary': '#e7e7e8',
  '--secondary-foreground': '#383a42',
  '--muted': '#eaebeb',
  '--muted-foreground': '#909196',
  '--faint': '#eeeeef',
  '--faint-foreground': '#aeaeb2',
  '--accent': '#d9dfed',
  '--accent-foreground': '#383a42',
  '--danger': '#e45649',
  '--danger-foreground': '#000000',
  '--success': '#50a14f',
  '--success-foreground': '#000000',
  '--warning': '#c18401',
  '--warning-foreground': '#000000',
  '--border': '#d8d8d9',
  '--input': '#d5d6d7',
  '--ring': '#4078f2',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(250, 250, 250, 0.5)',
  '--lm-danger-subtle': '#fae0de',
  '--lm-success-subtle': '#defade',
  '--lm-warning-subtle': '#faf1de',
  '--lm-icon-monochrome': '#686a70',
}

const dark = {
  '--background': '#282c34',
  '--foreground': '#aeb5c2',
  '--card': '#21252b',
  '--card-foreground': '#aeb5c2',
  '--primary': '#61afef',
  '--primary-foreground': '#000000',
  '--secondary': '#383c45',
  '--secondary-foreground': '#aeb5c2',
  '--muted': '#353a42',
  '--muted-foreground': '#707681',
  '--faint': '#2f333b',
  '--faint-foreground': '#5c616b',
  '--accent': '#384957',
  '--accent-foreground': '#aeb5c2',
  '--danger': '#e06c75',
  '--danger-foreground': '#000000',
  '--success': '#98c379',
  '--success-foreground': '#000000',
  '--warning': '#e5c07b',
  '--warning-foreground': '#000000',
  '--border': '#3e4452',
  '--input': '#41454f',
  '--ring': '#61afef',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(40, 44, 52, 0.5)',
  '--lm-danger-subtle': '#4e2a2d',
  '--lm-success-subtle': '#394e2a',
  '--lm-warning-subtle': '#4e412a',
  '--lm-icon-monochrome': '#8c929e',
  '--lm-opencode-inner': '#434750',
  '--lm-opencode-outer': '#c6cbd4',
}

// The sixteen ANSI colours, the dark set is the `Atom One Dark` scheme, whose slots 1-6 equal the
// hues converted from colors.less exactly. The LIGHT set is ours, derived from
// one-light-syntax/colors.less: the collection's `Atom One Light` collapses
// cyan onto green and black onto brightBlack, so it is not used. The six
// CHROMATIC brights repeat their normal slots in both variants, which is what
// the upstream dark scheme does. The achromatic four do not: brightBlack is a
// dim grey in each set, and one-light's brightWhite is the page background.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#383a42',
  red: '#e45649',
  green: '#50a14f',
  yellow: '#c18401',
  blue: '#4078f2',
  magenta: '#a626a4',
  cyan: '#0184bc',
  white: '#a0a1a7',
  brightBlack: '#696c77',
  brightRed: '#e45649',
  brightGreen: '#50a14f',
  brightYellow: '#c18401',
  brightBlue: '#4078f2',
  brightMagenta: '#a626a4',
  brightCyan: '#0184bc',
  brightWhite: '#fafafa',
}

const darkAnsi = {
  black: '#21252b',
  red: '#e06c75',
  green: '#98c379',
  yellow: '#e5c07b',
  blue: '#61afef',
  magenta: '#c678dd',
  cyan: '#56b6c2',
  white: '#abb2bf',
  brightBlack: '#767676',
  brightRed: '#e06c75',
  brightGreen: '#98c379',
  brightYellow: '#e5c07b',
  brightBlue: '#61afef',
  brightMagenta: '#c678dd',
  brightCyan: '#56b6c2',
  brightWhite: '#abb2bf',
}

export const oneTheme: ThemeDefinition = {
  id: 'one',
  label: 'One',
  variants: [
    {
      id: 'one-light',
      label: 'One Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'one-light',
    },
    {
      id: 'one-dark',
      label: 'One Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'one-dark-pro',
    },
  ],
  defaults: { light: 'one-light', dark: 'one-dark' },
  credit: {
    project: 'One',
    url: 'https://github.com/atom/one-dark-syntax',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
