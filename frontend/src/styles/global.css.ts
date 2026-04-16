// Base CSS tokens (--space-*, --radius-*, --font-size-*, --line-height-*, etc.)
// are provided by @knadh/oat — see node_modules/@knadh/oat/css/01-theme.css
// for the full list of custom properties and their values.

import { globalFontFace, globalStyle } from '@vanilla-extract/css'

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Regular.woff2") format("woff2")',
  fontWeight: 400,
  fontStyle: 'normal',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Bold.woff2") format("woff2")',
  fontWeight: 700,
  fontStyle: 'normal',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Italic.woff2") format("woff2")',
  fontWeight: 400,
  fontStyle: 'italic',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-BoldItalic.woff2") format("woff2")',
  fontWeight: 700,
  fontStyle: 'italic',
  fontDisplay: 'swap',
})

globalStyle('html, body, #app', {
  height: '100%',
  width: '100%',
  overflow: 'hidden',
})

// LeapMux color scheme overrides (light theme)
globalStyle(':root', {
  vars: {
    // Core palette — warm sand base
    '--background': 'rgb(253 252 250)',
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

    // Typography — wire user-configurable fonts into Oat's variables
    '--font-sans': `var(--ui-font-family, system-ui, sans-serif)`,
    '--font-mono': `var(--mono-font-family, "Hack NF", Hack, "SF Mono", Consolas, monospace)`,

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
  },
})

// LeapMux color scheme overrides (dark theme)
globalStyle('[data-theme="dark"]', {
  vars: {
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
  },
})

// Override Oat's code/pre background (var(--faint)) with a semi-transparent
// foreground tint so it blends naturally on any surface.
globalStyle('code, pre', {
  backgroundColor: 'rgb(from var(--foreground) r g b / 0.075)',
})

// Prevent double background when code/pre are nested.
globalStyle('pre code, pre pre, code pre, code code', {
  backgroundColor: 'transparent',
})

// Reduce hr margin inside dropdown menus (Oat base sets var(--space-8) = 2rem).
globalStyle('ot-dropdown hr', {
  margin: 'var(--space-2) 0',
})

// Remove italic from blockquotes (Oat default).
globalStyle('blockquote', {
  fontStyle: 'normal',
})

// Enable native width/height: auto transitions (progressive enhancement).
globalStyle(':root', {
  interpolateSize: 'allow-keywords',
} as any)

// Extend Oat button transitions to include color, border-color, and width.
globalStyle('button, [role="button"]', {
  'transition': 'background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), transform var(--transition-fast), width var(--transition-fast)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

// Consistent thin scrollbars across browsers (standard CSS — Firefox & Chrome 121+).
globalStyle('*', {
  scrollbarWidth: 'thin',
  scrollbarColor: 'var(--scrollbar-thumb) var(--scrollbar-track)',
})

// WebKit scrollbar styling (Safari & older Chrome).
globalStyle('*::-webkit-scrollbar', {
  width: '8px',
  height: '8px',
})

globalStyle('*::-webkit-scrollbar-track', {
  background: 'transparent',
})

globalStyle('*::-webkit-scrollbar-thumb', {
  backgroundColor: 'var(--scrollbar-thumb)',
  borderRadius: '4px',
  border: '2px solid transparent',
  backgroundClip: 'content-box',
})

globalStyle('*::-webkit-scrollbar-thumb:hover', {
  backgroundColor: 'var(--scrollbar-thumb-hover)',
})

globalStyle('*::-webkit-scrollbar-corner', {
  background: 'transparent',
})

// Prevent radio/checkbox inputs from shrinking inside flex containers.
globalStyle('input[type="radio"], input[type="checkbox"]', {
  flexShrink: 0,
})
