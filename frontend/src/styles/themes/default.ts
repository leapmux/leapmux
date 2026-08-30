// LeapMux's own palette: a warm sand light variant and a warm charcoal dark
// variant, both with a teal accent. This is the theme every other file in this
// directory is measured against -- `themes.test.ts` requires each theme to
// declare exactly the token set stated here.
//
// See ./types.ts for why these files stay plain data (one sanctioned
// exception: the generated palette import below).

import type { ThemeDefinition } from './types'

// The palette data is generated from contracts/theme-default.json (the same
// file the OAuth pages' inline palette is emitted from), imported by RELATIVE
// path: plain .ts, no alias, no vanilla-extract, so the bun-side consumers of
// this directory (scripts/generate-notice.mjs) resolve the chain without
// Vite. Everything else about the theme -- the variant metadata, the terminal
// colors -- stays written here.
import { dark, light } from '../../generated/contracts/theme-default'

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
