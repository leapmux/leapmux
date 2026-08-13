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

import { globalFontFace, globalLayer, globalStyle } from '@vanilla-extract/css'
import { darkPalette, lightPalette } from '~/styles/palette'
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
// consumers (DropdownMenu, Tooltip, GridSizePopover, the toast container)
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
// refuses the pan gesture entirely at the page level.
//
// A nested scroll region still pans: the browser resolves a touch's
// allowed behaviour against the ancestors up to the element that handles
// the gesture, which for a touch inside the chat list is that list, not
// the page. So the message list needs no opt-in, and it sets none —
// `pan-y` there would freeze the sideways scroll of every code block and
// table inside it, because a `touch-action` on an ANCESTOR restricts a
// descendant scroller.
//
// That is the rule for the whole app: a region declares `touch-action`
// only to constrain a gesture it owns, never to re-enable one. Three do.
// Two refuse a gesture — the scroll rail (`touchAction: 'none'`, see
// ~/components/chat/ChatScrollRail.css.ts), so a finger dragging its
// thumb scrubs instead of panning the list, and the tiling separator
// (see ~/components/shell/TilingLayout.css.ts), so a drag resizes the
// pane instead of scrolling. One narrows a gesture to the single axis it
// scrolls: the tab strip (`touchAction: 'pan-x'`, see
// ~/components/shell/TabBar.css.ts).
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
    ...lightPalette,

    // Typography — wire user-configurable fonts into Oat's variables. Not in
    // palette.ts: NOTICE.html has no preference store to read from, so it
    // declares its own literals instead of these indirections.
    '--font-sans': `var(--ui-font-family, system-ui, sans-serif)`,
    '--font-mono': `var(--mono-font-family, "Hack NF", Hack, "SF Mono", Consolas, monospace)`,
  },
})

// LeapMux color scheme overrides (dark theme)
globalStyle('[data-theme="dark"]', {
  vars: darkPalette,
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

// Menu items are `<button role="menuitem">`, so Oat's base button rule
// (button.css: `background-color: var(--primary)`, a 1px border, medium radius)
// applies to them. Through 0.6.x Oat's own `[role="menuitem"]` rule cancelled
// that with `background: none; border: none; color: var(--foreground)`; 0.7
// narrowed that rule to layout only (display/width/padding/justify/cursor) and
// dropped the cancelling half, which rendered every menu item as a solid
// primary-coloured button.
//
// These declarations are ours now rather than something we inherit: a menu item
// needs them to read as a menu item at all, and leaning on a third-party
// internal reset is what made a patch-level bump able to restyle every menu in
// the app.
//
// The layout half (display, width, padding, justify-content) is restated here
// for the same reason. Oat's own rule is an EXACT `[role="menuitem"]` match, so
// a `menuitemcheckbox` or `menuitemradio` never received it and fell back to
// Oat's `base` button rule instead -- 16px of horizontal padding beside a
// sibling's 12px, and `inline-flex` with centred content, which in an unclassed
// block `<menu>` (the TabBar menus) shrinks the item to its text and paints the
// hover accent over the label alone. Cursor still comes from Oat.
//
// They live in a LAYER because Oat's rule did. Oat declares
// `@layer theme,base,components,animations,utilities` and put `[role=menuitem]`
// in `components`, so every unlayered vanilla-extract class outranked it no
// matter what the selectors were. Restating the rule unlayered would have
// silently changed that: a bare `[role="menuitem"]` is (0,1,0), the same
// specificity as a VE class, so `dangerMenuItem` and the two `menuItemSelected`
// classes would have been decided by stylesheet link order alone -- and a
// chunking change would have flipped "Delete…" back to the default colour and
// erased the selected-row highlight, with nothing to catch it. Declared here,
// after Oat's statement, the layer appends past `utilities`: it still beats
// Oat's `base` button fill, and still loses to every unlayered class by
// construction. Scoping the selector instead would NOT work -- it raises
// specificity and would beat those classes outright.
const menuItem = globalLayer('leapmuxMenuItem')

// Prefix match, not an enumeration: it covers `menuitem`, `menuitemcheckbox`,
// and `menuitemradio`, and no other ARIA role begins with "menuitem". A list
// would silently drop a menu item's padding, alignment, and hover accent the
// first time an item takes the accurate checkbox or radio role.
globalStyle('[role^="menuitem"]', {
  '@layer': {
    [menuItem]: {
      display: 'flex',
      width: '100%',
      justifyContent: 'start',
      padding: 'var(--space-2) var(--space-3)',
      alignItems: 'center',
      gap: 'var(--space-2)',
      fontSize: 'var(--text-7)',
      textAlign: 'start',
      color: 'var(--foreground)',
      background: 'none',
      border: 'none',
      borderRadius: 'var(--radius-small)',
    },
  },
})

// Restated because the reset above cancels the background Oat's own hover rule
// would otherwise sit on top of, and this layer outranks Oat's `components`.
//
// `:not(:disabled, [aria-disabled="true"])` mirrors Oat's own
// `&:hover:not(:disabled)` on buttons. Menus here really do render disabled
// items -- BranchContextMenu's "Change branch…"/"Delete branch…",
// FileActionsMenu's download entries while busy, OpenInEditorButton's Refresh
// -- and Oat's `:disabled` rule sets only `cursor`/`opacity`, never
// `pointer-events: none`, so without the guard hovering one paints the accent
// that says "this will activate" on a control that will not.
globalStyle('[role^="menuitem"]:is(:hover, :focus):not(:disabled, [aria-disabled="true"])', {
  '@layer': {
    [menuItem]: {
      background: 'var(--accent)',
    },
  },
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
