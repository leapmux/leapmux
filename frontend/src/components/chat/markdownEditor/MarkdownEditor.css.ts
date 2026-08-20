import { globalStyle, style } from '@vanilla-extract/css'
import { codeBlockCode, codeBlockPre } from '~/styles/codeBlock'
import { popoverBase } from '~/styles/popover.css'
import { paginationContainer } from '../ControlRequestBanner.css'
import { codeSurface } from '../shikiTokenColors.css'

export const container = style({
  position: 'relative',
  display: 'flex',
  flexDirection: 'column',
  width: '100%',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  backgroundColor: 'var(--background)',
  overflow: 'hidden',
  // The composer button height: one line of text (font-size × line-height)
  // plus its top/bottom padding (space-1 × 2). Referenced by the `[+]` button,
  // the action buttons, the editor wrapper min-height, the separator position,
  // and the ProseMirror paddings — all derived from this single value.
  vars: {
    '--composer-btn-h': 'calc(var(--text-7) * var(--leading-normal) + var(--space-1) * 2)',
    // Collapsed-mode left padding of the text area: the `[+]` button's left
    // offset + its width + a gap. Declared here so the stylesheet below and
    // the expand/collapse measurement in MarkdownEditor.tsx read one value
    // instead of each spelling the same calc().
    '--composer-left-pad': 'calc(var(--space-1) + var(--composer-btn-h) + var(--space-1))',
  },
  selectors: {
    '&:focus-within': {
      borderColor: 'var(--ring)',
    },
  },
})

/**
 * The editor row holds the editor body and the two overlay slots: the `[+]`
 * button (left) and the action cluster (right). The slots are absolutely
 * positioned so they can hug the top corners when the content is a single
 * line, and the bottom corners once it expands — driven by the container's
 * `data-expanded` attribute. The editor's horizontal padding reserves room so
 * typed text never underlaps the overlays.
 */
export const editorRow = style({
  'position': 'relative',
  'display': 'flex',
  'flex': 1,
  // Center the editor wrapper vertically so the text/placeholder sits between
  // the top edge and the bottom-anchored buttons, with equal gaps above and below.
  // Min-height fits the button height (text-7 * leading-normal + space-1 * 2)
  // plus a space-1 gap above and below.
  'alignItems': 'center',
  'minHeight': 'calc(var(--composer-btn-h) + var(--space-1) * 2)',
  // The expanded state reserves the button row here (see below). Animating it
  // is what grows and shrinks the box as the layout mode flips.
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      transition: 'padding-bottom var(--transition)',
    },
  },
})

export const plusSlot = style({
  position: 'absolute',
  // Anchored at the bottom in BOTH states. The box grows/shrinks via the
  // editor row's animated padding-bottom, and the buttons ride the bottom
  // edge smoothly — no jump on expand or collapse. In collapsed mode the box
  // is short, so the buttons sit near the top (overlaid at the edges); in
  // expanded mode the box is taller and the buttons are in the bottom row.
  bottom: 'var(--space-1)',
  left: 'var(--space-1)',
  zIndex: 1,
  display: 'flex',
  alignItems: 'center',
  pointerEvents: 'none',
})

export const footerSlot = style({
  position: 'absolute',
  bottom: 'var(--space-1)',
  right: 'var(--space-1)',
  zIndex: 1,
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  pointerEvents: 'none',
})

// Children of the overlay slots re-enable pointer events (the slot itself is
// pass-through so the empty corners don't block the editor beneath).
globalStyle(`${plusSlot} > *`, {
  pointerEvents: 'auto',
})
globalStyle(`${footerSlot} > *`, {
  pointerEvents: 'auto',
})

// Constrain the ACTION buttons in the footer slot (Interrupt/Send and the
// control-request actions) to the single-line text area height so they match
// the `[+]` button.
//
// The pagination zone is excluded. Its items are square 22px page numbers, not
// action buttons, and this rule outranks them: `.footerSlot button` is (0,1,1)
// against `paginationItem`'s (0,1,0), so it would stretch each square to the
// button height and give it 16px of horizontal padding inside a 22px box,
// leaving no room for the digit. The exclusion specifies the CENTER zone rather
// than the item, so a future non-action control placed there keeps its own
// size too.
globalStyle(`${footerSlot} button:not(.${paginationContainer} button)`, {
  height: 'var(--composer-btn-h)',
  padding: '0 var(--space-2)',
  fontSize: 'var(--text-8)',
  lineHeight: 1,
  gap: 'var(--space-1)',
  borderRadius: 'var(--radius-small)',
})

// Expanded state: buttons stay anchored at bottom:var(--space-1) — the animated
// padding-bottom growth on the editor row moves them down naturally. Only z-index and
// pointer-events change (the buttons become interactive in expanded mode).
globalStyle(`${container}[data-expanded] ${plusSlot}`, {
  zIndex: 0,
  pointerEvents: 'auto',
})

globalStyle(`${container}[data-expanded] ${footerSlot}`, {
  zIndex: 0,
  justifyContent: 'flex-end',
  pointerEvents: 'auto',
})

// Control-request footers (full-width two-zone action rows) stretch across the
// box instead of hugging the right corner.
globalStyle(`${container}[data-expanded] ${footerSlot}[data-full-width]`, {
  left: 'var(--space-1)',
  right: 'var(--space-1)',
})

/**
 * The link edit popover: a URL field, Save, and remove, on one row.
 *
 * Sized so the row can never overflow. Everything here shrinks — the box caps
 * at a readable width and at the viewport, the form may shrink below its
 * content, and the field gives up width before the buttons do. The field used
 * to carry a fixed `220px` and the form could not shrink, so the row was wider
 * than the box that held it: with `overflow-y` inherited from the popover
 * chrome, CSS computes `overflow-x` to `auto` as well, and the card grew a
 * horizontal scrollbar with the remove button pushed out of view.
 */
export const linkPopover = style([popoverBase, {
  boxSizing: 'border-box',
  width: 'fit-content',
  maxWidth: 'min(28rem, calc(100vw - var(--space-4) * 2))',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  padding: 'var(--space-1)',
  boxShadow: 'var(--shadow-large)',
}])

export const linkPopoverForm = style({
  display: 'flex',
  gap: 'var(--space-1)',
  alignItems: 'center',
  // A flex item of the popover. Both are needed for it to shrink with the box
  // rather than push past it: `minWidth: 0` lifts the automatic min-content
  // floor, and the full width keeps it filling the card when there is room.
  minWidth: 0,
  width: '100%',
})

export const linkPopoverInput = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'fontSize': 'var(--text-7)',
  'padding': 'var(--space-1) var(--space-2)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-small)',
  'backgroundColor': 'var(--background)',
  'color': 'var(--foreground)',
  // The field absorbs the row's slack and yields first when the box is narrow,
  // so the two buttons stay reachable at any width. `all: unset` resets
  // `min-width` to `auto`, which would otherwise floor an input at its default
  // size and reintroduce the overflow.
  'flex': '1 1 14rem',
  'minWidth': 0,
  ':focus-visible': {
    borderColor: 'var(--ring)',
  },
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

export const codeLangPopoverContent = style([popoverBase, {
  // popoverBase supplies the UA-reset (position:fixed; margin:0 -- so calcPopoverPosition's
  // top/left place the popover at the trigger instead of margin:auto re-centering it) and
  // the `:popover-open`-gated `display: flex`. This adds the picker's own box.
  flexDirection: 'column',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-large)',
  width: '280px',
}])

export const editorWrapper = style({
  flex: 1,
  // Min-height matches one line of text (font-size * line-height) plus the
  // ProseMirror's top/bottom padding (space-1 * 2), so the editor wrapper is
  // exactly as tall as its content — no extra space to misalign the placeholder.
  minHeight: 'var(--composer-btn-h)',
  maxHeight: '50vh',
  overflowY: 'auto',
})

/**
 * Separator between the text area and the button row in expanded mode.
 * Absolutely positioned at the top of the button reservation (the editor row's
 * padding-bottom area). Transparent by default; fades to var(--border) in
 * expanded mode.
 */
export const editorSeparator = style({
  'position': 'absolute',
  'left': 0,
  'right': 0,
  // Sit one space-1 above the action row's top, so there's a gap between the
  // separator and the row. Row top = bottom(space-1) + the row's measured
  // height; the separator is space-1 above that. The fallback is one button
  // height, which is what the row measures until the ResizeObserver runs.
  'bottom': 'calc(var(--composer-actions-h, var(--composer-btn-h)) + var(--space-1) * 2)',
  'height': '1px',
  'backgroundColor': 'transparent',
  'pointerEvents': 'none',
  'zIndex': 0,
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      transition: 'background-color var(--transition)',
    },
  },
})

// Expanded state: the separator becomes visible.
globalStyle(`${container}[data-expanded] ${editorSeparator}`, {
  backgroundColor: 'var(--border)',
})

// ProseMirror editor layout. In the collapsed (single-line) state the left
// padding reserves room for the `[+]` button overlay (button width + its left
// offset + a gap) and the right padding reserves room for the action cluster
// (Interrupt + Send), so typed text never underlaps either. In the expanded
// state the `[+]` and actions sit on their own bottom row, so the text area
// drops to the normal small padding and uses the box's full width.
globalStyle(`${editorWrapper} .ProseMirror`, {
  'padding': 'var(--space-1) var(--composer-right-pad, 96px) var(--space-1) var(--composer-left-pad)',
  'outline': 'none',
  'minHeight': '20px',
  'whiteSpace': 'pre-wrap',
  'wordWrap': 'break-word',
  // Animate the horizontal padding as the layout mode flips. The buttons are
  // anchored at bottom:var(--space-1) in both states, so they ride the bottom
  // edge smoothly as the box animates.
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      transition: 'padding var(--transition)',
    },
  },
})

// Expanded state: controls drop to the bottom row, so the text area spans the
// full width with just the standard box padding.
globalStyle(`${container}[data-expanded] ${editorWrapper} .ProseMirror`, {
  padding: 'var(--space-1) var(--space-2)',
})

// Expanded state: reserve the button row on the ROW, not inside the scroll box.
// `editorWrapper` scrolls and clips at its padding edge, so padding on its
// scrolled child (the ProseMirror) only adds blank space at the END of the
// document -- scrolled up, the text runs under the buttons and the separator is
// drawn across it. The row is not a scroll container, so its padding-bottom
// takes that strip out of the scrollport instead.
//
// The reservation is: gap above separator (space-1) + separator + gap below
// separator to the row (space-1) + the row's height + bottom offset (space-1).
//
// The height is the MEASURED one (--composer-actions-h, written by
// MarkdownEditor.tsx from a ResizeObserver), not a fixed button height. The row
// is an absolutely positioned overlay anchored to the bottom edge, so a
// reservation shorter than the row does not grow the box -- the row grows
// upward over the text instead, and its opaque background hides the bottom of
// the editor line. A control request whose actions wrap to two lines (Claude's
// ExitPlanMode puts a column of approval switches beside Approve) does exactly
// that. The fallback covers the frames before the first measurement.
globalStyle(`${container}[data-expanded] ${editorRow}`, {
  paddingBottom: 'calc(var(--composer-actions-h, var(--composer-btn-h)) + var(--space-1) * 3)',
})

// Code blocks: move scroll to <code> so the language label stays fixed.
globalStyle(`${editorWrapper} .ProseMirror pre`, codeBlockPre('visible'))
globalStyle(`${editorWrapper} .ProseMirror pre code`, codeBlockCode)

// Code block language label -- absolutely positioned at top-right of <pre>
globalStyle(`${editorWrapper} .ProseMirror pre .code-lang-label`, {
  position: 'absolute',
  right: '4px',
  top: '4px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  padding: '1px 4px',
  borderRadius: 'var(--radius-small)',
  userSelect: 'none',
  zIndex: 1,
})

globalStyle(`${editorWrapper} .ProseMirror pre .code-lang-label:hover`, {
  backgroundColor: 'var(--card)',
  color: 'var(--muted-foreground)',
})

// Shiki syntax highlighting in editor code blocks (via prosemirror-highlight):
// the inline decorations carry --shiki-light / --shiki-dark CSS variables (color
// only -- the wrapper owns the bg).
codeSurface(`${editorWrapper} .ProseMirror pre`, 'block', [{ suffix: ' .shiki' }])

// Task list checkboxes (ProseMirror-specific)
globalStyle(`${editorWrapper} .ProseMirror li[data-checked]`, {
  listStyle: 'none',
  position: 'relative',
  marginLeft: '-20px',
  paddingLeft: '20px',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked]::before`, {
  position: 'absolute',
  left: 0,
  top: '2px',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked="true"]::before`, {
  content: '"\\2611"',
  color: 'var(--primary)',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked="false"]::before`, {
  content: '"\\2610"',
  color: 'var(--muted-foreground)',
})

globalStyle(`${editorWrapper} .ProseMirror .placeholder`, {
  color: 'var(--faint-foreground)',
  position: 'absolute',
  pointerEvents: 'none',
})

// Placeholder for empty editor
globalStyle(`${editorWrapper} .ProseMirror p.is-editor-empty:first-child::before`, {
  content: 'attr(data-placeholder)',
  color: 'var(--faint-foreground)',
  pointerEvents: 'none',
  float: 'left',
  height: 0,
})
