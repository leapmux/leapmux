// Gruvbox -- adapted from gruvbox (MIT).
// https://github.com/gruvbox-community/gruvbox
//
// Variants: gruvbox light and gruvbox dark, each at hard, medium and soft
// contrast
//
// light0/dark0 backgrounds with light0_soft/dark0_soft panels. Accents are
// the faded_* set on light and the bright_* set on dark, which is the
// pairing the upstream colorscheme uses for each background.
//
// The token roles and the contrast floors are stated in ./types.ts. Values are
// derived from the upstream palette by the shared rule described there, so this
// file reads the same as its ten siblings.

import type { ThemeDefinition } from './types'

const light = {
  '--background': '#fbf1c7',
  '--foreground': '#3c3836',
  '--card': '#f2e5bc',
  '--card-foreground': '#3c3836',
  '--primary': '#076678',
  '--primary-foreground': '#ffffff',
  '--secondary': '#e8deb8',
  '--secondary-foreground': '#3c3836',
  '--muted': '#ece2bb',
  '--muted-foreground': '#7c6f64',
  '--faint': '#f0e6be',
  '--faint-foreground': '#a09380',
  '--accent': '#b8d7dd',
  '--accent-foreground': '#3c3836',
  '--danger': '#9d0006',
  '--danger-foreground': '#ffffff',
  '--success': '#79740e',
  '--success-foreground': '#ffffff',
  '--warning': '#b57614',
  '--warning-foreground': '#000000',
  '--border': '#e5d09a',
  '--input': '#d5c4a1',
  '--ring': '#076678',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(251, 241, 199, 0.5)',
  '--lm-danger-subtle': '#f4b2b4',
  '--lm-success-subtle': '#f4f1b2',
  '--lm-warning-subtle': '#f4dab2',
  '--lm-icon-monochrome': '#5f564f',
}

const dark = {
  '--background': '#282828',
  '--foreground': '#ebdbb2',
  '--card': '#32302f',
  '--card-foreground': '#ebdbb2',
  '--primary': '#83a598',
  '--primary-foreground': '#000000',
  '--secondary': '#3f3d39',
  '--secondary-foreground': '#ebdbb2',
  '--muted': '#3c3a36',
  '--muted-foreground': '#928374',
  '--faint': '#32312f',
  '--faint-foreground': '#746a5f',
  '--accent': '#335045',
  '--accent-foreground': '#ebdbb2',
  '--danger': '#fb4934',
  '--danger-foreground': '#000000',
  '--success': '#b8bb26',
  '--success-foreground': '#000000',
  '--warning': '#fabd2f',
  '--warning-foreground': '#000000',
  '--border': '#494542',
  '--input': '#504945',
  '--ring': '#83a598',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(40, 40, 40, 0.5)',
  '--lm-danger-subtle': '#462926',
  '--lm-success-subtle': '#464626',
  '--lm-warning-subtle': '#463c26',
  '--lm-icon-monochrome': '#baab90',
  '--lm-opencode-inner': '#4f4c44',
  '--lm-opencode-outer': '#f1e6c9',
}

// The sixteen ANSI colours, from the `Gruvbox Light` and `Gruvbox Dark` schemes. The light one
// uses gruvbox's inverted mapping: ANSI black is light0, ANSI white is dark.
//
// FIFTEEN of the sixteen are the same for every contrast level, and slot 0 is
// not. Upstream sets `terminal_color_0` to `s:bg0` -- the one value the
// contrast axis moves -- and `terminal_color_7/8/15` to fg4/gray/fg1, which it
// does not. A shared slot 0 leaves ANSI black a near-miss of the terminal
// background on the hard and soft variants: 1.03:1 and 1.11:1, visible as a
// smudge rather than as either text or background.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift.
const lightAnsi = {
  black: '#fbf1c7',
  red: '#cc241d',
  green: '#98971a',
  yellow: '#d79921',
  blue: '#458588',
  magenta: '#b16286',
  cyan: '#689d6a',
  white: '#7c6f64',
  brightBlack: '#928374',
  brightRed: '#9d0006',
  brightGreen: '#79740e',
  brightYellow: '#b57614',
  brightBlue: '#076678',
  brightMagenta: '#8f3f71',
  brightCyan: '#427b58',
  brightWhite: '#3c3836',
}

const darkAnsi = {
  black: '#282828',
  red: '#cc241d',
  green: '#98971a',
  yellow: '#d79921',
  blue: '#458588',
  magenta: '#b16286',
  cyan: '#689d6a',
  white: '#a89984',
  brightBlack: '#928374',
  brightRed: '#fb4934',
  brightGreen: '#b8bb26',
  brightYellow: '#fabd2f',
  brightBlue: '#83a598',
  brightMagenta: '#d3869b',
  brightCyan: '#8ec07c',
  brightWhite: '#ebdbb2',
}

// ANSI black follows the contrast level, because upstream's slot 0 is bg0. The
// medium variants read `lightAnsi` / `darkAnsi` unchanged, whose slot 0 is
// already light0 / dark0.
const lightHardAnsi = { ...lightAnsi, black: '#f9f5d7' }
const lightSoftAnsi = { ...lightAnsi, black: '#f2e5bc' }
const darkHardAnsi = { ...darkAnsi, black: '#1d2021' }
const darkSoftAnsi = { ...darkAnsi, black: '#32302f' }

// Gruvbox light, hard contrast. The contrast axis moves the BACKGROUND only:
// upstream's `g:gruvbox_contrast_light` changes bg0 and nothing else, so all
// three light variants share `lightAnsi` above -- ANSI black excepted, because
// that slot IS bg0 -- and differ in the surface ramp this table derives from
// that background.
const lightHard = {
  '--background': '#f9f5d7',
  '--foreground': '#3c3836',
  '--card': '#f0eccf',
  '--card-foreground': '#3c3836',
  '--primary': '#076678',
  '--primary-foreground': '#ffffff',
  '--secondary': '#e6e2c7',
  '--secondary-foreground': '#3c3836',
  '--muted': '#eae6ca',
  '--muted-foreground': '#7c6f64',
  '--faint': '#eeeace',
  '--faint-foreground': '#9f9b8a',
  '--accent': '#c1dce1',
  '--accent-foreground': '#3c3836',
  '--danger': '#9d0006',
  '--danger-foreground': '#ffffff',
  '--success': '#79740e',
  '--success-foreground': '#ffffff',
  '--warning': '#b57614',
  '--warning-foreground': '#000000',
  '--border': '#e7d3a2',
  '--input': '#d5c4a1',
  '--ring': '#076678',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(249, 245, 215, 0.5)',
  '--lm-danger-subtle': '#f6bec0',
  '--lm-success-subtle': '#f6f3be',
  '--lm-warning-subtle': '#f6e0be',
  '--lm-icon-monochrome': '#5f5b54',
}

// Gruvbox light, soft contrast.
const lightSoft = {
  '--background': '#f2e5bc',
  '--foreground': '#3c3836',
  '--card': '#e9ddb6',
  '--card-foreground': '#3c3836',
  '--primary': '#076678',
  '--primary-foreground': '#ffffff',
  '--secondary': '#e0d4af',
  '--secondary-foreground': '#3c3836',
  '--muted': '#e4d7b1',
  '--muted-foreground': '#7c6f64',
  '--faint': '#e8dbb4',
  '--faint-foreground': '#9b937c',
  '--accent': '#aacfd6',
  '--accent-foreground': '#3c3836',
  '--danger': '#9d0006',
  '--danger-foreground': '#ffffff',
  '--success': '#79740e',
  '--success-foreground': '#ffffff',
  '--warning': '#b57614',
  '--warning-foreground': '#000000',
  '--border': '#dbc692',
  '--input': '#d5c4a1',
  '--ring': '#076678',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(242, 229, 188, 0.5)',
  '--lm-danger-subtle': '#f1a0a4',
  '--lm-success-subtle': '#f1eea0',
  '--lm-warning-subtle': '#f1d2a0',
  '--lm-icon-monochrome': '#5d584f',
}

// Gruvbox dark, hard contrast.
const darkHard = {
  '--background': '#1d2021',
  '--foreground': '#ebdbb2',
  '--card': '#282a28',
  '--card-foreground': '#ebdbb2',
  '--primary': '#83a598',
  '--primary-foreground': '#000000',
  '--secondary': '#353632',
  '--secondary-foreground': '#ebdbb2',
  '--muted': '#323330',
  '--muted-foreground': '#928374',
  '--faint': '#282a28',
  '--faint-foreground': '#6d695a',
  '--accent': '#2c453b',
  '--accent-foreground': '#ebdbb2',
  '--danger': '#fb4934',
  '--danger-foreground': '#000000',
  '--success': '#b8bb26',
  '--success-foreground': '#000000',
  '--warning': '#fabd2f',
  '--warning-foreground': '#000000',
  '--border': '#413d3b',
  '--input': '#504945',
  '--ring': '#83a598',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(29, 32, 33, 0.5)',
  '--lm-danger-subtle': '#3b2220',
  '--lm-success-subtle': '#3a3b20',
  '--lm-warning-subtle': '#3b3220',
  '--lm-icon-monochrome': '#b7ac8e',
  '--lm-opencode-inner': '#46453e',
  '--lm-opencode-outer': '#f1e1b6',
}

// Gruvbox dark, soft contrast.
const darkSoft = {
  '--background': '#32302f',
  '--foreground': '#ebdbb2',
  '--card': '#3b3936',
  '--card-foreground': '#ebdbb2',
  '--primary': '#83a598',
  '--primary-foreground': '#000000',
  '--secondary': '#48443e',
  '--secondary-foreground': '#ebdbb2',
  '--muted': '#45423c',
  '--muted-foreground': '#928374',
  '--faint': '#3b3936',
  '--faint-foreground': '#7a7362',
  '--accent': '#3a5a4e',
  '--accent-foreground': '#ebdbb2',
  '--danger': '#fb4934',
  '--danger-foreground': '#000000',
  '--success': '#b8bb26',
  '--success-foreground': '#000000',
  '--warning': '#fabd2f',
  '--warning-foreground': '#000000',
  '--border': '#534d49',
  '--input': '#554e4a',
  '--ring': '#83a598',
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',
  '--lm-bg-translucent': 'rgba(50, 48, 47, 0.5)',
  '--lm-danger-subtle': '#51302c',
  '--lm-success-subtle': '#51512c',
  '--lm-warning-subtle': '#51462c',
  '--lm-icon-monochrome': '#bdb091',
  '--lm-opencode-inner': '#575249',
  '--lm-opencode-outer': '#f1e0b6',
}

export const gruvboxTheme: ThemeDefinition = {
  id: 'gruvbox',
  label: 'Gruvbox',
  variants: [
    {
      id: 'gruvbox-light-hard',
      label: 'Hard',
      polarity: 'light',
      palette: lightHard,
      terminal: lightHardAnsi,
      syntax: 'gruvbox-light-hard',
    },
    {
      id: 'gruvbox-light-medium',
      label: 'Medium',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'gruvbox-light-medium',
    },
    {
      id: 'gruvbox-light-soft',
      label: 'Soft',
      polarity: 'light',
      palette: lightSoft,
      terminal: lightSoftAnsi,
      syntax: 'gruvbox-light-soft',
    },
    {
      id: 'gruvbox-dark-hard',
      label: 'Hard',
      polarity: 'dark',
      palette: darkHard,
      terminal: darkHardAnsi,
      syntax: 'gruvbox-dark-hard',
    },
    {
      id: 'gruvbox-dark-medium',
      label: 'Medium',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'gruvbox-dark-medium',
    },
    {
      id: 'gruvbox-dark-soft',
      label: 'Soft',
      polarity: 'dark',
      palette: darkSoft,
      terminal: darkSoftAnsi,
      syntax: 'gruvbox-dark-soft',
    },
  ],
  defaults: { light: 'gruvbox-light-medium', dark: 'gruvbox-dark-medium' },
  variantLabel: 'Contrast',
  credit: {
    project: 'gruvbox',
    url: 'https://github.com/gruvbox-community/gruvbox',
    license: 'MIT',
  },
  terminalCredit: {
    project: 'iTerm2-Color-Schemes',
    url: 'https://github.com/mbadolato/iTerm2-Color-Schemes',
    license: 'MIT',
  },
}
