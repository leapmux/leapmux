// Base CSS tokens are provided by @knadh/oat — see
// node_modules/@knadh/oat/css/01-theme.css for the full list of custom
// properties and their values. Common ones:
//   --space-{1..18}                spacing scale
//   --radius-{small,medium,large,full}
//   --text-{1..8}, --text-regular  font-size scale
//   --font-{normal,bold}           font-weight tokens (prefer these over
//                                  numeric weights; --font-medium and
//                                  --font-semibold also exist but our
//                                  convention is normal-or-bold only)
//   --leading-normal               default line-height
//   --shadow-{small,medium,large}
//   --transition, --transition-fast
//   --z-{dropdown,modal}

import { globalFontFace, globalStyle } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

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

// iOS Safari viewport lock.
//
//  1. `position: fixed` + `height: 100dvh` ties the body to the dynamic
//     visible viewport. `dvh` tracks iOS 16.4+ keyboard-up shrinkage on
//     its own, so we don't drive body height from JS.
//  2. `padding-top: env(safe-area-inset-top)` keeps content out from
//     under the system status bar in standalone PWA mode. No bottom
//     padding — the composer sits flush with the screen bottom and the
//     home indicator overlays it translucently (KakaoTalk-style).
//  3. `transform: translateY(calc(-1 * var(--vv-offset, 0px)))` cancels
//     the residual `visualViewport.offsetTop` that iOS 26 WebKit leaves
//     non-zero after keyboard dismiss (FB19889436). `window.scrollTo(0,0)`
//     can't fix this — body is `overflow: hidden`, there's nothing to
//     scroll. The hook only sets `--vv-offset` while the keyboard is
//     *down*; during keyboard-up iOS' own visual-viewport translate
//     brings the composer into view and a counter-translate would
//     double-shift.
//
// Note: the body's `transform` makes body the containing block for
// descendant `position: fixed` elements. Native HTML `popover` API
// consumers (DropdownMenu, Tooltip, GridSizePopover, LinkPopover)
// escape via the top layer. The one non-top-layered fixed consumer is
// `SelectionQuotePopover`, which counter-translates by
// `var(--vv-offset, 0px)` to stay viewport-relative.
globalStyle('body', {
  position: 'fixed',
  top: 0,
  left: 0,
  width: '100%',
  height: '100dvh',
  paddingTop: 'env(safe-area-inset-top)',
  // NO padding-bottom: keep KakaoTalk-style intrusion (composer flush
  // with screen bottom, home-indicator translucently overlaying it).
  boxSizing: 'border-box',
  // iOS-26 mitigation: cancel any residual visualViewport.offsetTop the
  // OS leaves non-zero after keyboard dismiss. Default 0 on every other
  // platform; the hook only sets it when it's actually non-zero.
  transform: 'translateY(calc(-1 * var(--vv-offset, 0px)))',
  willChange: 'transform',
})

// Kill iOS Safari's rubber-band overscroll on the page itself.
// `overscroll-behavior` alone isn't enough on iOS WebKit — the bounce
// is dispatched below that layer. `touch-action: none` on html+body
// refuses the pan gesture entirely at the page level; inner scroll
// regions opt back in (e.g. messageList → `pan-y`).
globalStyle('html, body', {
  overscrollBehavior: 'none',
  touchAction: 'none',
})

// Mobile form-control font-size floor. iOS Safari (browser + standalone
// PWA) auto-zooms when focusing an `<input>` / `<textarea>` / `<select>`
// whose computed font-size is < 16px. The zoom is NOT undone on blur
// and persists across in-app navigations (e.g. after submitting the
// login form), leaving the user on the next screen at ~1.15x scale with
// no easy way back. Anchoring form-control font-size at 16px on mobile
// is the standard suppression and does not affect desktop styling or
// disable user pinch-zoom.
globalStyle('input, textarea, select', {
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // iOS auto-zoom threshold; must be >= 16 CSS px. Hard-coded
      // because this is a WebKit-imposed value, not a design choice —
      // `rem` would scale with the user's root font-size and could fall
      // below the threshold, and Oat's `--text-*` tokens track design
      // intent for body text, which is a different concern.
      fontSize: '16px',
    },
  },
})

// LeapMux color scheme overrides (light theme)
globalStyle(':root', {
  vars: {
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

// Blockquotes: keep Oat's italic, but lift the color from the faint
// --muted-foreground to roughly halfway toward body text, so the quote reads
// clearly without being as loud as normal text.
globalStyle('blockquote', {
  color: 'color-mix(in oklab, var(--foreground), var(--muted-foreground))',
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

// Render the focus ring inside the element so it is never clipped by an
// ancestor with overflow: hidden. Outline color and thickness still come
// from Oat's :focus-visible rule (2px solid var(--ring)); we only flip the
// offset from +2px (outside) to -2px (inside).
globalStyle(':focus-visible', {
  outlineOffset: '-2px',
})

// Add a 1px --background-colored ring just inside the focus outline on
// Oat-styled buttons so the teal outline stays distinguishable when the
// button itself is filled with --primary / --secondary / --danger. Uses
// var(--background) so the inner ring is invisible on surfaces that share
// the page background (where no separator is needed).
globalStyle(
  'button:focus-visible, [type="submit"]:focus-visible, [type="reset"]:focus-visible, [type="button"]:focus-visible, a.button:focus-visible, ::file-selector-button:focus-visible',
  {
    boxShadow: 'inset 0 0 0 2px var(--background)',
  },
)
