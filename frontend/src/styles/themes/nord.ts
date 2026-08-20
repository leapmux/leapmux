// Nord -- adapted from Nord (MIT).
// https://github.com/nordtheme/nord
//
// Variants: Polar Night (dark). BOTH LIGHT PALETTES ARE OURS.
//
// Nord ships no light theme. The light variants here are built from Nord's
// own ramps rather than invented: Snow Storm (nord4-6) supplies the
// backgrounds, Polar Night (nord0-3) the text, and Frost/Aurora the accents.
// They are a derivation, not an upstream palette -- NOTICE says so.
//
// The two light variants differ in the page background only, the way gruvbox's
// contrast axis does. Snow Storm paints nord6; Snow Storm Brighter paints white
// and moves the whole surface ramp one step out, so nord6 becomes the card
// rather than the page. Their syntax themes are the two flavours that
// huytd/vscode-nord-light publishes -- see ~/lib/syntaxThemes -- which is what
// gives the brighter variant a light half of its own rather than a second
// palette over one document.
//
// Nord assigns each Aurora colour a role: nord11 errors, nord13 warnings,
// nord14 success. nord12 is annotations and decorators, NOT warnings -- it
// sits 20 degrees from nord11, so a warning drawn in it reads as a second
// red. The light variant therefore takes nord13 for --warning, darkened to
// #c5a565 for a Snow Storm background exactly as `lightAnsi` below already
// darkens it; --success takes nord14 darkened to #96b17f the same way. The
// dark variant needs neither move and carries both unchanged.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#eceff4',
  '--foreground': '#2e3440',
  '--card': '#e5e9f0',
  '--card-foreground': '#2e3440',
  '--primary': '#5e81ac',
  '--primary-foreground': '#000000',
  '--secondary': '#d9dce2',
  '--secondary-foreground': '#2e3440',
  '--muted': '#dde0e6',
  '--muted-foreground': '#4c566a',
  '--faint': '#e1e4e9',
  '--faint-foreground': '#798191',
  '--accent': '#ccd8e6',
  '--accent-foreground': '#2e3440',
  '--danger': '#bf616a',
  '--danger-foreground': '#000000',
  '--success': '#96b17f',
  '--success-foreground': '#000000',
  '--warning': '#c5a565',
  '--warning-foreground': '#000000',
  '--border': '#c8d0e0',
  '--input': '#c8cbd2',
  '--ring': '#5e81ac',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(236, 239, 244, 0.5)',
  '--lm-danger-subtle': '#f8ccd0',
  '--lm-success-subtle': '#e0f8cc',
  '--lm-warning-subtle': '#f8e9cc',
  '--lm-icon-monochrome': '#3e4757',
}

// Snow Storm on white. The page leaves the Snow Storm ramp entirely and the
// surfaces move out one step to fill the gap: nord6 becomes --card, nord5 the
// far end of the ramp, and --border/--input darken to keep clearing it. The
// text, the accents and the subtle fills are `light` above unchanged -- the
// contrast against white is higher than against nord6, so every floor that
// variant clears this one clears by more.
const lightBrighter = {
  '--background': '#ffffff',
  '--foreground': '#2e3440',
  '--card': '#eceff4',
  '--card-foreground': '#2e3440',
  '--primary': '#5e81ac',
  '--primary-foreground': '#000000',
  '--secondary': '#e5e9f0',
  '--secondary-foreground': '#2e3440',
  '--muted': '#e9edf3',
  '--muted-foreground': '#4c566a',
  '--faint': '#f2f4f8',
  '--faint-foreground': '#798191',
  '--accent': '#d6e2f0',
  '--accent-foreground': '#2e3440',
  '--danger': '#bf616a',
  '--danger-foreground': '#000000',
  '--success': '#96b17f',
  '--success-foreground': '#000000',
  '--warning': '#c5a565',
  '--warning-foreground': '#000000',
  '--border': '#d3dae6',
  '--input': '#d3d7de',
  '--ring': '#5e81ac',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(255, 255, 255, 0.5)',
  '--lm-danger-subtle': '#f8ccd0',
  '--lm-success-subtle': '#e0f8cc',
  '--lm-warning-subtle': '#f8e9cc',
  '--lm-icon-monochrome': '#3e4757',
}

const dark = {
  '--background': '#2e3440',
  '--foreground': '#eceff4',
  '--card': '#3b4252',
  '--card-foreground': '#eceff4',
  '--primary': '#88c0d0',
  '--primary-foreground': '#000000',
  '--secondary': '#454a56',
  '--secondary-foreground': '#eceff4',
  '--muted': '#414752',
  '--muted-foreground': '#7b88a1',
  '--faint': '#383d49',
  '--faint-foreground': '#657086',
  '--accent': '#3f5a62',
  '--accent-foreground': '#eceff4',
  '--danger': '#bf616a',
  '--danger-foreground': '#000000',
  '--success': '#a3be8c',
  '--success-foreground': '#000000',
  '--warning': '#ebcb8b',
  '--warning-foreground': '#000000',
  '--border': '#475164',
  '--input': '#4c566a',
  '--ring': '#88c0d0',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(46, 52, 64, 0.5)',
  '--lm-danger-subtle': '#5a3034',
  '--lm-success-subtle': '#435a30',
  '--lm-warning-subtle': '#5a4c30',
  '--lm-icon-monochrome': '#aeb6c6',
  '--lm-opencode-inner': '#545964',
  '--lm-opencode-outer': '#f2f4f7',
}

// The sixteen ANSI colours, from the `Nord Light` and `Nord` schemes. Unlike the UI palette, the
// light ANSI set is NOT our derivation -- the collection carries one.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#3b4252',
  red: '#bf616a',
  green: '#96b17f',
  yellow: '#c5a565',
  blue: '#81a1c1',
  magenta: '#b48ead',
  cyan: '#7bb3c3',
  white: '#a5abb6',
  brightBlack: '#4c566a',
  brightRed: '#bf616a',
  brightGreen: '#96b17f',
  brightYellow: '#c5a565',
  brightBlue: '#81a1c1',
  brightMagenta: '#b48ead',
  brightCyan: '#82afae',
  brightWhite: '#eceff4',
}

const darkAnsi = {
  black: '#3b4252',
  red: '#bf616a',
  green: '#a3be8c',
  yellow: '#ebcb8b',
  blue: '#81a1c1',
  magenta: '#b48ead',
  cyan: '#88c0d0',
  white: '#e5e9f0',
  brightBlack: '#596377',
  brightRed: '#bf616a',
  brightGreen: '#a3be8c',
  brightYellow: '#ebcb8b',
  brightBlue: '#81a1c1',
  brightMagenta: '#b48ead',
  brightCyan: '#8fbcbb',
  brightWhite: '#eceff4',
}

export const nordTheme: ThemeDefinition = {
  id: 'nord',
  label: 'Nord',
  variants: [
    {
      id: 'nord-light',
      label: 'Snow Storm',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'nord-light',
    },
    {
      id: 'nord-light-brighter',
      label: 'Snow Storm Brighter',
      polarity: 'light',
      palette: lightBrighter,
      terminal: lightAnsi,
      syntax: 'nord-light-brighter',
    },
    {
      id: 'nord-dark',
      label: 'Polar Night',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'nord',
    },
  ],
  defaults: { light: 'nord-light', dark: 'nord-dark' },
  variantLabel: 'Variant',
  credit: {
    project: 'Nord',
    url: 'https://github.com/nordtheme/nord',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
