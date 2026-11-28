// LeapMux's own palette: a warm sand light variant and a warm charcoal dark
// variant, both with a teal accent. This is the theme every other file in this
// directory is measured against -- `themes.test.ts` requires each theme to
// declare exactly the token set stated here.
//
// See ./types.ts for why these files stay plain data (one sanctioned
// exception: the generated palette import below).

import type { ThemeDefinition } from './types'

// The palette and Dimidium 16-color terminal ANSI data are generated from
// contracts/theme-default.json (the same file the OAuth pages' inline palette
// and the docs site's cli-cast terminal theme read from), imported by RELATIVE
// path: plain .ts, no alias, no vanilla-extract, so the bun-side consumers of
// this directory (scripts/generate-notice.mjs) resolve the chain without Vite.
//
// Dimidium (Zlib, https://github.com/dofuuz/dimidium) is terminal-only; its UI
// counterpart is THIS theme. The terminal's background, foreground, cursor, and
// selection stay in `light`/`dark` so `resolveTerminalTheme` derives them from
// one source of truth instead of two that can drift.
import { dark, darkAnsi, light, lightAnsi } from '../../generated/contracts/theme-default'

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
