import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { breakpoints, motion } from '~/styles/tokens'

// Dialog container

export const standard = style({
  'position': 'relative',
  'minWidth': '360px',
  'maxWidth': '900px',
  'display': 'flex',
  'flexDirection': 'column',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      minWidth: 'unset',
      maxWidth: '100vw',
      width: '100vw',
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
  'height': '80vh',
  '@media': {
    '(max-width: 479px)': {
      height: '100vh',
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
globalStyle(`${standard} > .${body} > form`, {
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${standard} > .${body} > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
})

globalStyle(`${standard} > .${body} > form > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${standard} > .${body} > form > section > .vstack`, {
  flex: 1,
  minHeight: 0,
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

export const twoColumn = style({
  'display': 'grid',
  'gridTemplateColumns': '1fr 1fr',
  'gap': 'var(--space-4)',
  'flex': 1,
  'minHeight': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      gridTemplateColumns: '1fr',
    },
  },
})

export const singleColumn = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

export const leftPanel = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflow: 'hidden',
  gap: 'var(--space-4)',
})

export const rightPanel = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflowY: 'auto',
  gap: 'var(--space-4)',
})

// In two-column layout, the grid and its left panel must fill remaining height.
globalStyle(`${standard} > .${body} > form > section > .vstack > .${twoColumn}`, {
  flex: 1,
  minHeight: 0,
})

// Form utilities

export const labelRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const treeContainer = style({
  flex: 1,
  minHeight: 0,
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflow: 'hidden',
})

// The element wrapping the DirectoryTree needs to grow and use flex layout.
globalStyle(`${standard} > .${body} > form > section > .vstack :has(> .${treeContainer})`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
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
