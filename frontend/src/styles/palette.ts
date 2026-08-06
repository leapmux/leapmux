// The LeapMux colour palette, as plain data.
//
// Two consumers need these values and only one of them can execute CSS:
// global.css.ts spreads them into `:root` / `[data-theme="dark"]`, and
// scripts/generate-notice.mjs inlines them into the standalone NOTICE.html
// page. That script runs under bun with no Vite and no vanilla-extract, so it
// cannot import a `.css.ts` module -- hence a plain `.ts` file with no imports,
// which both sides can read.
//
// Typography is deliberately NOT here. The app wires --font-sans/--font-mono
// through `var(--ui-font-family, ...)` so a user preference can override them;
// the notice page has no preference store, so it declares its own. Each owner
// states its own fonts next to where it uses them.

/** Light theme (`:root`). */
export const lightPalette: Record<string, string> = {
  // Core palette — warm sand base
  '--background': 'rgb(255 254 252)',
  '--foreground': 'rgb(34 32 30)',
  '--card': 'rgb(247 245 242)',
  '--card-foreground': 'rgb(34 32 30)',

  // Primary — teal accent
  '--primary': 'rgb(13 148 136)',
  '--primary-foreground': 'rgb(255 255 255)',

  // Secondary — warm sand
  '--secondary': 'rgb(232 230 225)',
  '--secondary-foreground': 'rgb(46 43 40)',

  // Muted
  '--muted': 'rgb(237 235 231)',
  '--muted-foreground': 'rgb(120 117 111)',

  // Faint — subtler than muted
  '--faint': 'rgb(242 240 236)',
  '--faint-foreground': 'rgb(160 157 151)',

  // Accent — soft sage green
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
  '--lm-bg-translucent': 'rgba(255, 255, 255, 0.5)',
  '--lm-danger-subtle': 'rgb(253 235 233)',
  '--lm-success-subtle': 'rgb(236 247 220)',
  '--lm-warning-subtle': 'rgb(254 245 221)',
  '--lm-icon-monochrome': 'rgb(101 99 99)',

}

/** Dark theme (`[data-theme="dark"]`). */
export const darkPalette: Record<string, string> = {
  // Core palette — warm charcoal base
  '--background': 'rgb(26 25 23)',
  '--foreground': 'rgb(232 230 225)',
  '--card': 'rgb(42 40 38)',
  '--card-foreground': 'rgb(232 230 225)',

  // Primary — brighter teal for dark bg
  '--primary': 'rgb(20 184 166)',
  '--primary-foreground': 'rgb(12 12 11)',

  // Secondary
  '--secondary': 'rgb(51 48 45)',
  '--secondary-foreground': 'rgb(224 221 216)',

  // Muted
  '--muted': 'rgb(46 43 40)',
  '--muted-foreground': 'rgb(138 134 128)',

  // Faint — subtler than muted
  '--faint': 'rgb(36 34 32)',
  '--faint-foreground': 'rgb(107 104 98)',

  // Accent — soft sage green
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
  '--lm-opencode-inner': '#4B4646',
  '--lm-opencode-outer': '#F1ECEC',

}
