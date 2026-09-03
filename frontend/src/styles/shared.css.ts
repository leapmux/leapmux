import { style } from '@vanilla-extract/css'
import { declareAppLayers } from '~/styles/layers'

const layers = declareAppLayers()

/**
 * Removes what the browser and Oat paint on a control: the UA button
 * appearance, its font and its border, and Oat's solid primary pill.
 *
 * The reset sits in the `reset` LAYER, and that is what makes it safe to
 * compose. An unlayered declaration beats a layered one whatever the
 * specificity, and whatever the stylesheet order, so a class that composes this
 * one keeps every property that it declares itself. Unlayered, the two rules
 * tie -- one class each -- and the winner is then decided by which stylesheet
 * the bundler emits last. `axisChip` in
 * `~/components/chat/composer/composer.css.ts` lost that race: `all: unset`
 * erased its own `font-size`, `line-height` and `white-space`, and the chip
 * rendered at the ambient 16px with no ellipsis.
 *
 * The layer still beats Oat, whose button rule sits in Oat's `base` layer, and
 * it beats the UA sheet, which every author declaration outranks.
 */
export const controlReset = style({
  '@layer': {
    [layers.reset]: { all: 'unset' },
  },
})

/**
 * The small muted button that opens something: the composer's status-bar chips
 * (branch, model, effort, mode) and the info cluster beside them (the
 * context-usage trigger, the copy buttons inside its card).
 *
 * They sit next to each other in the same bar, so a divergence in radius, hover
 * colour, or resting colour is immediately visible. Each composer takes this
 * base and adds only what genuinely differs: its padding, its font size, and
 * whether it centres its content.
 */
export const chipBase = style([controlReset, {
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  color: 'var(--faint-foreground)',
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--card)' },
  },
}])

export const errorText = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})

export const successText = style({
  color: 'var(--success)',
  fontSize: 'var(--text-7)',
})

export const warningText = style({
  color: 'var(--warning)',
  fontSize: 'var(--text-7)',
})

export const emptyState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  fontStyle: 'italic',
})

/**
 * A label that stays on ONE line: the browser clips the overflow at the right
 * edge and marks it with an ellipsis.
 *
 * `min-width: 0` is what lets a flex item shrink below the width of its own
 * text. Without it the item keeps its content width, the row grows instead, and
 * the ellipsis never appears -- the container scrolls sideways.
 *
 * Clipping HIDES text, so the full string needs another route to the reader.
 * Pair this with `<Tooltip showWhen="clipped">`, which shows the tooltip only
 * while the label is actually clipped. `ClippedText` in
 * `~/components/common/ClippedText` pairs the two, and is what a caller should
 * reach for.
 *
 * Take this style directly in these three cases only, and record which one
 * applies at the site:
 * 1. The label is not a plain string. It is arbitrary JSX, or it must hold a
 *    child element such as a link.
 * 2. A tooltip cannot fire. The label sits under a `disabled` ancestor, which
 *    receives no pointer events, or it is a drag image.
 * 3. The rule must reach an element that `ClippedText` does not render -- a
 *    `globalStyle` on a class that a rehype plugin emits, for example.
 *
 * A caller that renders through `ClippedText` must NOT compose this style into
 * the class it passes. The component already applies it, and a second owner
 * makes the removal of either one invisible.
 */
export const clippedText = style({
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

/**
 * The small round status light that a sidebar row carries at its right end.
 *
 * Shape only. Each section supplies its own palette, because the states differ:
 * a worker is connected or disconnected, a background task is queued, running,
 * succeeded, failed, or stopped. The SHAPE is shared so that the two sections
 * read as one vocabulary rather than as two similar dots that drift apart.
 */
export const statusDot = style({
  width: 8,
  height: 8,
  borderRadius: '50%',
  flexShrink: 0,
})

// Menu utilities

export const dangerMenuItem = style({
  color: 'var(--danger)',
})

/**
 * A menu item that OPENS A SUBMENU: its label at one end, its chevron at the
 * other.
 *
 * Shared because every submenu trigger in the app is this same shape -- the
 * composer's `[+]` groups, the sidebar's section menu, the workspace row menu.
 * It lived in the composer's own stylesheet while the sidebar's copy carried a
 * `class="sub-trigger"` that matched no rule at all, so one surface was styled
 * and the other silently was not.
 */
export const menuSubTrigger = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  width: '100%',
})

/**
 * The leading half of a submenu trigger, for an item whose label carries an
 * icon (the branch item). `menuSubTrigger` pushes its two children apart, so
 * the icon and the text need one box between them or the chevron separates
 * them instead.
 */
export const menuSubTriggerLabel = style([clippedText, {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
}])

export const menuSectionHeader = style({
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  padding: 'var(--space-1) var(--space-3)',
})

export const menuItemContent = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  width: '100%',
  minWidth: 0,
})

/**
 * The slot at the RIGHT end of a menu item: a short note that the row keeps
 * whole while the label beside it clips.
 *
 * Two kinds of note go in it -- a keyboard shortcut, and the age of the thing
 * the row names. It is named for the SLOT rather than for either one, because a
 * class called `menuItemShortcut` on a timestamp makes every reader decide
 * which meaning applies.
 *
 * `flex-shrink: 0` is the whole point. The label takes the squeeze and ends in
 * an ellipsis; the note stays readable at any label length.
 */
export const menuItemDetail = style({
  marginLeft: 'auto',
  flexShrink: 0,
  color: 'var(--muted-foreground)',
  opacity: 0.75,
  fontSize: 'var(--text-8)',
  whiteSpace: 'nowrap',
})

// Layout utilities

export const inlineFlex = style({
  display: 'inline-flex',
})

export const centeredFull = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
})

export const heightFull = style({
  height: '100%',
})

/**
 * The full-page card surface: Oat's `card` fill, border, radius and shadow,
 * plus the one width every centered full-page card uses. Composes Oat's
 * `card` as a plain class name (vanilla-extract accepts one in a style
 * list), the same mechanic as `floatingCardSurface` in
 * `~/styles/popover.css.ts`.
 */
export const pageCard = style(['card', { width: '480px' }])

/**
 * Painted nowhere, still in the document and still reachable by anything that
 * reads the page.
 *
 * NOT `display: none` and NOT `visibility: hidden`. Both take the element out
 * of the layout entirely, and everything that walks the RENDERED page then
 * stops seeing it. Three callers depend on exactly that difference:
 *
 *   - The `autocomplete="username"` hint a re-authentication form owes a
 *     password manager, so it knows which saved credential to fill (see
 *     `ElevateForm`). A manager that walks the rendered fields skips a field
 *     with no box.
 *   - The description a `<Tooltip>` leaves beside a DISABLED control. A
 *     disabled element dispatches no pointer event and takes no focus, so the
 *     tooltip itself can never open there, and this is what a screen reader
 *     reads instead.
 *   - The plain-text copy of a widget whose visual DOM reads as meaningless
 *     text -- the thinking odometer's stack of 0-9 strips.
 *
 * `position: absolute` keeps it out of flow, so it is not a flex or grid item
 * and cannot open a gap in the row it sits in.
 *
 * Whether a screen reader announces it is the CALLER's to decide. A field
 * nobody types into adds `aria-hidden`; a description that exists FOR a
 * screen reader must not.
 */
export const srOnly = style({
  position: 'absolute',
  width: '1px',
  height: '1px',
  padding: 0,
  margin: '-1px',
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
  border: 0,
})
