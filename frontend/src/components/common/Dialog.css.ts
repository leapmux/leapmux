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

// The dialog enters the top layer the instant `[open]` is set, with
// no prior state to transition from -- the modern fix is
// `@starting-style` + `transition-behavior: allow-discrete`, but
// vanilla-extract's at-rule registry only knows `@media`, `@supports`,
// `@container`, and `@layer`, so there is no clean way to emit
// `@starting-style { ... }` rules from `.css.ts` files. Open is
// driven by `@keyframes` keyed off the `[open]` selector (the
// animation property is added to a matching element, the animation
// fires from frame 0). Close is orchestrated in `Dialog.tsx`: a
// `closing` marker class triggers an exit keyframe and the actual
// `dialogRef.close()` is deferred until the animation has finished
// playing -- without that orchestration the UA would yank the dialog
// out of the top layer synchronously and the exit keyframe would
// never run.
// Pure opacity fade -- no translate. A centered dialog has no natural
// origin direction; a slide reads as positional imprecision rather
// than intentional motion, especially against the fixed `wide`/`tall`
// dimensions that lock the dialog's box.
const dialogEnter = keyframes({
  from: { opacity: 0 },
  to: { opacity: 1 },
})

const dialogExit = keyframes({
  from: { opacity: 1 },
  to: { opacity: 0 },
})

const backdropEnter = keyframes({
  from: { backgroundColor: 'rgba(0, 0, 0, 0)' },
  to: { backgroundColor: 'rgba(0, 0, 0, 0.4)' },
})

const backdropExit = keyframes({
  from: { backgroundColor: 'rgba(0, 0, 0, 0.4)' },
  to: { backgroundColor: 'rgba(0, 0, 0, 0)' },
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

// Marker class applied by `Dialog.tsx` for the brief window between
// the user initiating a close and the actual `dialogRef.close()`
// call. The exit keyframes run during this window.
export const closing = style({})

// `prefers-reduced-motion: reduce` override spread into each animated
// rule below so the four sites don't each redeclare the same nested
// media block.
const animationOff = {
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
    },
  },
}

// `animation-fill-mode: both` closes the one-frame window where the
// element could paint at its non-animation state (opacity: 1) before
// the keyframe animation's `from` (opacity: 0) takes effect. Without
// it the dialog briefly flashes at full opacity before fading in.
globalStyle(`.${standard}[open]`, {
  animation: `${dialogEnter} ${motion.fast}ms ease-out both`,
  ...animationOff,
})

globalStyle(`.${standard}.${closing}[open]`, {
  animation: `${dialogExit} ${motion.fast}ms ease-in both`,
  ...animationOff,
})

globalStyle(`.${standard}::backdrop`, {
  backgroundColor: 'rgba(0, 0, 0, 0.4)',
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
  gap: '6px',
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
