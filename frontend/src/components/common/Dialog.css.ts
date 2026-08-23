import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { breakpoints, motion } from '~/styles/tokens'

// Dialog container

// Safe-area env() values. Resolve to 0px on ordinary desktop browsers; on an
// iOS/Android PWA (or iPhone Safari) with `viewport-fit=cover` they report the
// status bar / Dynamic Island / notch / home indicator / display cutout.
const SAFE_TOP = 'env(safe-area-inset-top, 0px)'
const SAFE_RIGHT = 'env(safe-area-inset-right, 0px)'
const SAFE_BOTTOM = 'env(safe-area-inset-bottom, 0px)'
const SAFE_LEFT = 'env(safe-area-inset-left, 0px)'
// Prefer dvh: on iOS Safari `vh` is the LARGE viewport and overshoots when
// browser chrome is visible (same reason `huge` uses dvh on the phone band).
const SAFE_MAX_HEIGHT = `calc(100dvh - ${SAFE_TOP} - ${SAFE_BOTTOM})`
const SAFE_MAX_HEIGHT_OAT = `calc(85vh - ${SAFE_TOP} - ${SAFE_BOTTOM})`

// Oat caps every dialog at `max-height: 85vh` from `@layer components`, and a
// max-height beats `tall`'s `height: 100vh` — so without the raise below, the
// full-screen phone treatment rendered at 85vh with a strip of backdrop above
// and below. Unlayered author CSS outranks Oat's layer without a specificity
// fight, the same mechanic `~/styles/popover.css.ts` records for the card
// padding.
export const standard = style({
  'position': 'relative',
  'minWidth': '360px',
  'maxWidth': '900px',
  'display': 'flex',
  'flexDirection': 'column',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      minWidth: 'unset',
      maxWidth: '100%',
      width: '100%',
      // Height cap lives on `dialog.${standard}:modal` below so it can also
      // subtract the safe-area insets. A bare `100vh` here would let the panel
      // cover the status bar in a standalone PWA.
    },
  },
})

// Modal dialogs live in the TOP LAYER, so they escape `body`'s
// `padding-top: env(safe-area-inset-top)` (see ~/styles/global.css.ts). Without
// this rule a phone-band dialog paints under the status bar / Dynamic Island
// in an iOS standalone PWA (`viewport-fit=cover` + `black-translucent`), and
// the close button is untappable. The same insets cover landscape notches,
// the iPad home indicator, and Android display cutouts.
//
// Selector is `dialog.<standard>:modal` so its specificity (0,2,1) beats the
// UA's `dialog:modal { inset: 0 }` (0,1,1). `height: fit-content` stops
// top+bottom from stretching a short confirm dialog to fill the safe
// rectangle (abspos height:auto stretch); `tall` / `huge` override it below.
// The ::backdrop still paints the full viewport — only the panel insets.
globalStyle(`dialog.${standard}:modal`, {
  'top': SAFE_TOP,
  'right': SAFE_RIGHT,
  'bottom': SAFE_BOTTOM,
  'left': SAFE_LEFT,
  'height': 'fit-content',
  'maxHeight': SAFE_MAX_HEIGHT_OAT,
  'margin': 'auto',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // Fill the safe rectangle's width (left+right are set above).
      // `width: 100%` would be 100% of the viewport and overflow the
      // horizontal insets on a landscape notched phone.
      width: 'auto',
      maxWidth: '100%',
      maxHeight: SAFE_MAX_HEIGHT,
    },
  },
})

// Re-impose `display: none` when the dialog is not in the top layer.
// `.standard`'s `display: flex` author rule beats the UA's
// `dialog:not([open]) { display: none }`, so without this rule the
// dialog briefly paints in normal flow between component mount and
// the `showModal()` call inside `onMount` -- visible as a flash at
// the top-left of the page on dialogs whose initial content varies
// in size (e.g. anything carrying `AgentProviderSelector`).
globalStyle(`.${standard}:not([open])`, {
  display: 'none',
})

// Dialog open: the entry animation (opacity 0 -> 1, transform
// scale(0.95) -> scale(1)) is supplied by @knadh/oat's `dialog.css`
// via `@starting-style` + Oat's `transition: opacity 150ms,
// transform 150ms, ...`. We don't ship our own @keyframes for the
// open path because running both simultaneously caused a visible
// double-fade flash.
//
// Dialog close: Solid removes the dialog from the DOM as soon as the
// parent's `<Show>` flips, so any exit transition tied to `[open]`
// being removed never gets to play. We drive the exit by toggling a
// `.closing` marker class on the dialog (while [open] is still set)
// that overrides Oat's `:is([open])` values back to the @starting-
// style values (opacity 0, transform scale(0.95)) -- Oat's existing
// transition on `opacity`/`transform` animates the change. This
// stays inside a single transition pipeline so the open-time flicker
// doesn't return.
//
// Backdrop: Oat declares an opacity transition for `dialog::backdrop`
// in its @layer animations rule, but the transition isn't consistently
// honored across browsers (in particular WebKit, where the dialog
// snapped to its dimmed state without a fade). We drive the backdrop
// fade with our own keyframe targeting `background-color` instead of
// `opacity`, which is independent of Oat's opacity transition and
// runs reliably in every browser we ship to.

const backdropEnter = keyframes({
  from: { backgroundColor: 'rgba(0, 0, 0, 0)' },
  to: { backgroundColor: 'rgba(0, 0, 0, 0.5)' },
})

const backdropExit = keyframes({
  from: { backgroundColor: 'rgba(0, 0, 0, 0.5)' },
  to: { backgroundColor: 'rgba(0, 0, 0, 0)' },
})

// Marker class applied by `Dialog.tsx` for the brief window between
// the user initiating a close and the actual `dialogRef.close()`
// call. Drives both:
//   - the dialog's fade-out (rule below: overrides Oat's open-state
//     opacity/transform back to the @starting-style values, which
//     Oat's existing transition animates),
//   - the backdrop's fade-out keyframe (`backdropExit`).
export const closing = style({})

// Pin the dialog back to Oat's @starting-style values once .closing
// flips. Oat's `transition: opacity 150ms, transform 150ms, ...`
// (from `@layer components`) animates the change to those values
// over `motion.fast` ms; `Dialog.tsx` delays the unmount by the same
// duration so the transition has time to complete.
globalStyle(`.${standard}.${closing}[open]`, {
  opacity: 0,
  transform: 'scale(0.95)',
})

// Settle the OPEN dialog on `transform: none`, not on Oat's `scale(1)`.
//
// A transform of any kind -- `scale(1)` included, which computes to the
// identity matrix and paints nothing -- makes the element a CONTAINING BLOCK
// for its `position: fixed` descendants. Every menu inside a dialog is one:
// `DropdownMenu` positions its popover with viewport coordinates from
// `calcPopoverPosition`, so a containing block that is not the viewport
// silently changes what those coordinates mean.
//
// It stays hidden while the menu is open, because an open popover is in the TOP
// LAYER, and the top layer resolves against the viewport whatever the ancestors
// say. The moment the popover leaves the top layer on dismiss, the same
// unchanged `left` re-resolves against the dialog and the menu jumps right by
// the dialog's own left offset -- 464px at a 1440px width. Chromium hides the
// jump because `overlay ... allow-discrete` keeps the popover in the top layer
// for the whole close transition; the macOS desktop app's WKWebView paints a
// frame or two outside it, and that frame is the visible jump.
//
// The animation is unaffected. `none` interpolates as the identity matrix, so
// Oat's transition still runs `scale(0.95)` -> `none` on open and the
// `.closing` rule above still runs `none` -> `scale(0.95)` on close --
// confirmed in both Chromium and WebKit.
//
// `:not(.closing)` keeps this out of the exit: that rule needs its scale, and
// the two selectors carry equal specificity, so an overlap would be decided by
// source order rather than by intent.
globalStyle(`.${standard}[open]:not(.${closing})`, {
  transform: 'none',
})

const animationOff = {
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
    },
  },
}

// `animation-fill-mode: both` closes the one-frame window where the
// element could paint at its non-animation state (e.g. the dim
// `rgba(0,0,0,0.5)` background) before the keyframe's `from` (alpha
// 0) takes effect. Without it the backdrop briefly flashes fully dim
// before fading in from transparent. Matches Oat's
// `dialog:is([open])::backdrop { background-color: rgb(0 0 0 / 0.5) }`.
globalStyle(`.${standard}[open]::backdrop`, {
  backgroundColor: 'rgba(0, 0, 0, 0.5)',
  animation: `${backdropEnter} ${motion.fast}ms ease-out both`,
  ...animationOff,
})

globalStyle(`.${standard}.${closing}[open]::backdrop`, {
  animation: `${backdropExit} ${motion.fast}ms ease-in both`,
  ...animationOff,
})

export const wide = style({
  width: 'min(900px, 90vw)',
})

export const tall = style({
  // Desktop height only. The phone-band and safe-area overrides live on
  // `dialog.${standard}.${tall}:modal` below — they must beat the base
  // `:modal { height: fit-content }` rule (specificity 0,2,1).
  height: '80vh',
})

// Beat `dialog.${standard}:modal { height: fit-content }` so tall dialogs
// still fill their band, and subtract the safe-area insets so the panel
// cannot cover the status bar / home indicator.
globalStyle(`dialog.${standard}.${tall}:modal`, {
  'height': `calc(80vh - ${SAFE_TOP} - ${SAFE_BOTTOM})`,
  'maxHeight': `calc(80vh - ${SAFE_TOP} - ${SAFE_BOTTOM})`,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      height: SAFE_MAX_HEIGHT,
      maxHeight: SAFE_MAX_HEIGHT,
    },
  },
})

// The Preferences dialog's near-full-screen size. BOTH maxes are required:
// Oat caps every dialog at `max-height: 85vh` from `@layer components`, and a
// max-height beats `height`, so without the raise the 820px height renders
// clamped with a strip of backdrop above and below (the same mechanic the
// `standard` header comment records for the phone band).
export const huge = style({
  'width': 'min(1200px, 92vw)',
  'maxWidth': 'min(1200px, 92vw)',
  'height': 'min(820px, 88vh)',
  'maxHeight': 'min(820px, 88vh)',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // 100% of the viewport containing block, not 100vw: 100vw includes
      // the scrollbar gutter and overflows the screen by that strip.
      // Height/maxHeight for the phone band live on the `:modal` rule below
      // so they subtract safe-area insets and beat `height: fit-content`.
      width: '100%',
      maxWidth: '100%',
      minWidth: 0,
      overflow: 'hidden',
    },
  },
})

globalStyle(`dialog.${standard}.${huge}:modal`, {
  // Keep the desktop cap, but never taller than the safe rectangle.
  'height': `min(820px, calc(88vh - ${SAFE_TOP} - ${SAFE_BOTTOM}))`,
  'maxHeight': `min(820px, calc(88vh - ${SAFE_TOP} - ${SAFE_BOTTOM}))`,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // `dvh` (via SAFE_MAX_HEIGHT), not `vh`: this rule FORCES a height and
      // then clips at it. On iOS Safari `vh` resolves against the LARGE
      // viewport, so with the browser chrome shown the dialog is taller than
      // the space it has and `overflow: hidden` cuts the bottom off.
      width: 'auto',
      maxWidth: '100%',
      height: SAFE_MAX_HEIGHT,
      maxHeight: SAFE_MAX_HEIGHT,
    },
  },
})

export const header = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 0,
  padding: 'var(--space-4) var(--space-6) 0',
})

export const closeButton = style({
  position: 'absolute',
  top: 'var(--space-6)',
  right: 'var(--space-6)',
})

globalStyle(`${header} > h2`, {
  margin: 0,
})

// Dialog body wrapper provides consistent padding for all dialog content.
// The body has tabindex=-1 so it can absorb initial focus on dialog open
// without routing focus to the close button or a form control. Suppress
// its focus outline since it is only ever focused programmatically.
export const body = style({
  display: 'flex',
  flexDirection: 'column',
  flex: '1 1 auto',
  minHeight: 0,
  minWidth: 0,
  overflow: 'hidden',
  padding: 'var(--space-6)',
  paddingBlockStart: 'var(--space-4)',
  outline: 'none',
})

// Footer inside dialog body
globalStyle(`${standard} > .${body} > footer, ${standard} > .${body} > form > footer`, {
  display: 'flex',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
  paddingBlockStart: 'var(--space-6)',
})

// Make dialog forms use flex layout so the tree container can fill remaining space.
//
// The negative inline margin + matching padding bleeds the form out to the
// body's edges so the SECTION below can put its scrollbar at the dialog's edge
// instead of inside the body's padding. The pair of declarations keeps every
// other form child (the footer) at its old inset. Clipping stays sound:
// `overflow` clips at the padding box, which spans the bleed, so neither this
// box's `overflow: hidden` nor the body's cuts it off.
globalStyle(`${standard} > .${body} > form`, {
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  flex: 1,
  minHeight: 0,
  marginInline: 'calc(-1 * var(--space-6))',
  paddingInline: 'var(--space-6)',
})

// Same bleed for the bare (form-less) section, carrying the scroller directly
// in the padded body. The section repaints the inset itself.
globalStyle(`${standard} > .${body} > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
  marginInline: 'calc(-1 * var(--space-6))',
  paddingInline: 'var(--space-6)',
})

// The section is the dialog's ONE scroll container, in both the form-wrapped
// and bare shapes. The `overflowY` was missing here for the form shape for a
// long time unnoticed: desktop never scrolled the section, because the panels
// scrolled their own slices inside the fixed-height fill chain. The phone
// band's single-scroll layout depends on this — without it the section lets
// its content spill `visible` straight under the footer, with no scrollbar.
//
// The bleed-and-repad pair, one level deeper than the form's own, lands the
// section's border box on the body's edges — the scrollbar draws at the
// dialog's edge, and the padding restores the content inset.
globalStyle(`${standard} > .${body} > form > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
  marginInline: 'calc(-1 * var(--space-6))',
  paddingInline: 'var(--space-6)',
})

globalStyle(`${standard} > .${body} > form > section > .vstack`, {
  '@media': {
    [`(min-width: ${breakpoints.sm}px)`]: {
      flex: 1,
      minHeight: 0,
    },
  },
})

// Layout: top section

export const topSection = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-4)',
})

export const topTwoColumn = style({
  'display': 'grid',
  'gridTemplateColumns': '1fr 1fr',
  'gap': 'var(--space-4)',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      gridTemplateColumns: '1fr',
    },
  },
})

// Layout: column area
//
// The fill rules from here down are DESKTOP-ONLY. The desktop model partitions
// the dialog's fixed height: each level fills what is left of it, and the
// panels scroll their own slice. The phone model is one scroll — the section
// itself, over stacked content — and there every level below the section must
// size to its CONTENT: a `flex: 1; minHeight: 0` level resolves its basis-0
// children against the fixed height instead, a row sized shorter than its
// panel lets the panel's (no longer clipped) content spill into the row below
// — the overlapping panels this scoping fixed — and the surplus never reaches
// the section as scrollable height, so no scrollbar appeared.

export const twoColumn = style({
  'display': 'grid',
  'gridTemplateColumns': '1fr 1fr',
  'gap': 'var(--space-4)',
  'flex': 1,
  'minHeight': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // A flex column, not the one-column grid: grid rows still partition the
      // container's fixed height (auto tracks stretch to fill it), which is
      // the desktop model wearing a phone costume. Flex items with no grow
      // factor size to content and simply stack; the surplus becomes the
      // section's scroll.
      display: 'flex',
      flexDirection: 'column',
      flex: 'none',
    },
  },
})

export const singleColumn = style({
  'display': 'flex',
  'flexDirection': 'column',
  'flex': 1,
  'minHeight': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      flex: 'none',
    },
  },
})

export const leftPanel = style({
  'display': 'flex',
  'flexDirection': 'column',
  'minHeight': 0,
  // Desktop: `auto`, not `hidden` — the tree's minHeight floor can exceed the
  // space the dialog has left for this panel, and that excess must scroll, not
  // clip. Phone band: `visible` — the columns have stacked by then, and one
  // scroll for the whole dialog body reads better than two scrollers piled in
  // a column, so the panel gives up scrolling and lets its content flow into
  // the section (the body's one scroll container; the title header and the
  // footer sit outside it and stay pinned).
  'overflowY': 'auto',
  'gap': 'var(--space-4)',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      overflowY: 'visible',
    },
  },
})

export const rightPanel = style({
  'display': 'flex',
  'flexDirection': 'column',
  'minHeight': 0,
  // Scrolls on desktop for the same reason as the left panel; in the phone
  // band it defers to the section's single scroll, same as the left panel.
  'overflowY': 'auto',
  'gap': 'var(--space-4)',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      overflowY: 'visible',
    },
  },
})

// In two-column layout, the grid and its left panel must fill remaining height.
globalStyle(`${standard} > .${body} > form > section > .vstack > .${twoColumn}`, {
  '@media': {
    [`(min-width: ${breakpoints.sm}px)`]: {
      flex: 1,
      minHeight: 0,
    },
  },
})

// Form utilities

export const labelRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

// A flex COLUMN, not a block: the box holds the path input, the optional
// flavor hint, and the tree. As a block it would give the tree its full height
// beside those siblings, push the last rows past the bottom edge, and clip them
// out of reach behind `overflow: hidden`.
//
// The minHeight is a floor for the whole box. Every ancestor up to the
// fixed-height `tall` dialog clamps itself with `minHeight: 0`, so a
// `minHeight: 0` here let the tree be squeezed to a couple of rows whenever
// the viewport ran short (small windows, tall top sections, the stacked
// single-column layout). 240px keeps roughly eight tree rows (~25px each,
// after the path input and the tree's own padding take theirs); a panel too
// small to spare that scrolls instead of clipping (see `leftPanel`).
//
// The phone-band maxHeight pairs with the panels giving up their scrollboxes
// there: nothing bounds this box once the panel flows into the section's
// scroll, and an unbounded tree unrolls its whole listing into that scroll —
// the reader would page past every expanded folder to reach the git options
// below. Capped, it stays a compact list widget scrolling inside its border.
// 40vh sits near the 240px floor on phone-portrait viewports; on shorter
// (landscape) ones the floor wins and the box holds at eight rows.
export const treeContainer = style({
  'display': 'flex',
  'flexDirection': 'column',
  'flex': 1,
  'minHeight': '240px',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-medium)',
  'overflow': 'hidden',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      maxHeight: '40vh',
    },
  },
})

/** For a lone child of {@link treeContainer} that should fill the box. */
export const treeContainerFill = style({
  flex: 1,
  minHeight: 0,
})

// The element wrapping the DirectoryTree needs to grow and use flex layout.
// Desktop-only like the other fill rules; on the phone band Oat's own `vstack`
// (a flex column of content-sized items) is exactly what the single-scroll
// layout wants, and this rule's `flex: 1` would re-introduce the basis-0
// child the left row mis-sized.
globalStyle(`${standard} > .${body} > form > section > .vstack :has(> .${treeContainer})`, {
  '@media': {
    [`(min-width: ${breakpoints.sm}px)`]: {
      display: 'flex',
      flexDirection: 'column',
      flex: 1,
      minHeight: 0,
    },
  },
})

export const pathPreview = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  wordBreak: 'break-all',
})

export const radioGroup = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const radioRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  cursor: 'pointer',
})

export const radioSubContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  paddingLeft: 'var(--space-6)',
})
