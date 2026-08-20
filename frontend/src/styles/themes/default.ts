// LeapMux's own palette: a warm sand light variant and a warm charcoal dark
// variant, both with a teal accent. This is the theme every other file in this
// directory is measured against -- `themes.test.ts` requires each theme to
// declare exactly the token set stated here.
//
// See ./types.ts for why these files are plain data with no outside imports.

import type { ThemeDefinition } from './types'

const light = {
  // Core palette -- warm sand base
  '--background': 'rgb(255 254 252)',
  '--foreground': 'rgb(34 32 30)',
  '--card': 'rgb(247 245 242)',
  '--card-foreground': 'rgb(34 32 30)',

  // Primary -- teal accent
  '--primary': 'rgb(13 148 136)',
  '--primary-foreground': 'rgb(255 255 255)',

  // Secondary -- warm sand
  '--secondary': 'rgb(232 230 225)',
  '--secondary-foreground': 'rgb(46 43 40)',

  // Muted
  '--muted': 'rgb(237 235 231)',
  '--muted-foreground': 'rgb(120 117 111)',

  // Faint -- subtler than muted
  '--faint': 'rgb(242 240 236)',
  '--faint-foreground': 'rgb(160 157 151)',

  // Accent -- soft sage green
  '--accent': 'rgb(222 235 225)',
  '--accent-foreground': 'rgb(34 32 30)',

  // Semantic colors
  '--danger': 'rgb(220 74 68)',
  '--danger-foreground': 'rgb(255 255 255)',
  '--success': 'rgb(101 163 13)',
  '--success-foreground': 'rgb(255 255 255)',
  '--warning': 'rgb(245 158 11)',
  '--warning-foreground': 'rgb(34 32 30)',

  // Borders and interactive
  '--border': 'rgb(221 217 211)',
  '--input': 'rgb(213 209 203)',
  '--ring': 'rgb(13 148 136)',

  // Scrollbar
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',

  // LeapMux-specific custom variables
  '--lm-bg-translucent': 'rgba(255, 254, 252, 0.5)',
  '--lm-danger-subtle': 'rgb(253 235 233)',
  '--lm-success-subtle': 'rgb(236 247 220)',
  '--lm-warning-subtle': 'rgb(254 245 221)',
  '--lm-icon-monochrome': 'rgb(101 99 99)',
}

const dark = {
  // Core palette -- warm charcoal base
  '--background': 'rgb(26 25 23)',
  '--foreground': 'rgb(232 230 225)',
  '--card': 'rgb(42 40 38)',
  '--card-foreground': 'rgb(232 230 225)',

  // Primary -- brighter teal for dark bg
  '--primary': 'rgb(20 184 166)',
  '--primary-foreground': 'rgb(12 12 11)',

  // Secondary
  '--secondary': 'rgb(51 48 45)',
  '--secondary-foreground': 'rgb(224 221 216)',

  // Muted
  '--muted': 'rgb(46 43 40)',
  '--muted-foreground': 'rgb(138 134 128)',

  // Faint -- subtler than muted
  '--faint': 'rgb(36 34 32)',
  '--faint-foreground': 'rgb(107 104 98)',

  // Accent -- soft sage green
  '--accent': 'rgb(45 62 50)',
  '--accent-foreground': 'rgb(232 230 225)',

  // Semantic colors
  '--danger': 'rgb(239 83 80)',
  '--danger-foreground': 'rgb(255 255 255)',
  '--success': 'rgb(132 204 22)',
  '--success-foreground': 'rgb(12 12 11)',
  '--warning': 'rgb(251 191 36)',
  '--warning-foreground': 'rgb(26 25 23)',

  // Borders and interactive
  '--border': 'rgb(61 58 54)',
  '--input': 'rgb(61 58 54)',
  '--ring': 'rgb(20 184 166)',

  // Scrollbar
  '--scrollbar-thumb': 'rgb(from var(--muted-foreground) r g b / 0.35)',
  '--scrollbar-thumb-hover': 'rgb(from var(--muted-foreground) r g b / 0.55)',
  '--scrollbar-track': 'transparent',

  // LeapMux-specific custom variables
  '--lm-bg-translucent': 'rgba(26, 25, 23, 0.5)',
  '--lm-danger-subtle': 'rgb(50 30 28)',
  '--lm-success-subtle': 'rgb(28 38 20)',
  '--lm-warning-subtle': 'rgb(46 40 24)',
  '--lm-icon-monochrome': 'rgb(190 187 183)',

  // Dark-only on purpose. AgentProviderIcon.tsx reads these as
  // `var(--lm-opencode-inner, #CFCECD)`, so the light value lives at the point
  // of use. themes.test.ts allows a dark-only token and forbids a light-only
  // one, and requires every theme to make the same choice this one does.
  '--lm-opencode-inner': '#4B4646',
  '--lm-opencode-outer': '#F1ECEC',
}

// The sixteen ANSI colours, from the Dimidium terminal color scheme (Zlib) --
// https://github.com/dofuuz/dimidium
//
// Dimidium is terminal-only: it ships configs for alacritty, kitty, ghostty and
// a dozen others, and no editor or UI theme. Its UI counterpart is THIS theme,
// and always was -- the terminal background, foreground, cursor and selection
// were already Default's --background/--foreground/--primary/--accent before
// the palettes were split out. So Dimidium is not a theme of its own here; it
// is what Default's terminal looks like.
//
// The terminal's background, foreground, cursor and selection are NOT here.
// `resolveTerminalTheme` takes them from the palette above, so one theme states
// one background instead of two that can drift -- which they had: the light
// terminal background said #fdfcfa while --background said rgb(255 254 252).
const lightAnsi = {
  black: '#000000',
  red: '#b83d41',
  green: '#4d9833',
  yellow: '#ba8300',
  blue: '#0464ba',
  magenta: '#9c50bd',
  cyan: '#019a9f',
  white: '#9c9998',
  brightBlack: '#737575',
  brightRed: '#e0532e',
  brightGreen: '#1fbd62',
  brightYellow: '#d0a803',
  brightBlue: '#4a74ed',
  brightMagenta: '#d05dce',
  brightCyan: '#19b8d0',
  brightWhite: '#b8bdbe',
}

const darkAnsi = {
  black: '#000000',
  red: '#cf494c',
  green: '#60b442',
  yellow: '#db9c11',
  blue: '#0575d8',
  magenta: '#af5ed2',
  cyan: '#1db6bb',
  white: '#bab7b6',
  brightBlack: '#817e7e',
  brightRed: '#ff643b',
  brightGreen: '#37e57b',
  brightYellow: '#fccd1a',
  brightBlue: '#688dfd',
  brightMagenta: '#ed6fe9',
  brightCyan: '#32e0fb',
  brightWhite: '#dee3e4',
}

export const defaultTheme: ThemeDefinition = {
  id: 'default',
  label: 'Default',
  // Default's terminal is Dimidium's and its highlighting is GitHub's, so both
  // pickers name the palette the user is actually choosing. See ThemeDefinition.,
  terminalLabel: 'Default (Dimidium)',
  syntaxLabel: 'Default (GitHub)',
  variants: [
    {
      id: 'default-light',
      label: 'Light',
      polarity: 'light',
      palette: light,
      terminal: lightAnsi,
      syntax: 'github-light',
    },
    {
      id: 'default-dark',
      label: 'Dark',
      polarity: 'dark',
      palette: dark,
      terminal: darkAnsi,
      syntax: 'github-dark',
    },
  ],
  defaults: { light: 'default-light', dark: 'default-dark' },
  terminalCredit: {
    project: 'Dimidium',
    url: 'https://github.com/dofuuz/dimidium',
    license: 'Zlib',
  },
}
