import { style } from '@vanilla-extract/css'
import { touchScrollContain } from '~/styles/popover.css'
import { breakpoints } from '~/styles/tokens'

/**
 * Styles for `FilterableListbox` (`./FilterableListbox.tsx`).
 *
 * They live here rather than in `~/components/chat/markdownEditor/MarkdownEditor.css.ts`
 * (their first home) because three unrelated features render the widget: the composer's
 * settings chips, the composer's `[+]` submenus, and the code-language popover.
 * `FilterableListbox` applies them itself, so no caller passes them in and no
 * caller can forget one.
 */

export const comboboxControl = style({
  display: 'flex',
  alignItems: 'center',
  // Stay pinned at the foot of the popover when the listbox shrinks above a mobile keyboard.
  flexShrink: 0,
  padding: 'var(--space-1) var(--space-2)',
  borderTop: '1px solid var(--border)',
})

export const comboboxInput = style({
  'all': 'unset',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'width': '100%',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // Match the mobile 16px floor in `~/styles/global.css.ts` so class specificity
      // does not drop `<input>` below 16px and trigger iOS Safari auto-zoom.
      fontSize: '16px',
    },
  },
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

/**
 * The filterable option list.
 *
 * `height` with `flex: 0 1 auto` and `minHeight: 0` holds a steady 200px while
 * the user types — so narrowing the filter does not shrink the popover out from
 * under the pointer — while still letting the list shrink inside a flex-column
 * popover whose own `max-height` is shorter than 200px (for example, above the
 * iOS on-screen keyboard).
 *
 * `overscrollBehavior: 'contain'` keeps touch scroll at the ends of this list
 * from chaining into the host popover, and `touchAction: 'pan-y'` narrows touch
 * panning inside the list to its vertical scroll axis.
 */
const COMBOBOX_LISTBOX_HEIGHT = '200px'

export const comboboxListbox = style([
  touchScrollContain,
  {
    height: COMBOBOX_LISTBOX_HEIGHT,
    flex: '0 1 auto',
    minHeight: 0,
    overflowY: 'auto',
    overflowX: 'hidden',
    touchAction: 'pan-y',
    padding: 'var(--space-1)',
  },
])

export const comboboxItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-7)',
  padding: 'var(--space-1) var(--space-2)',
  cursor: 'pointer',
  color: 'var(--foreground)',
  borderRadius: 'var(--radius-small)',
})

/**
 * Primary option text inside `comboboxItem`.
 *
 * `minWidth: 0` with `overflow: 'hidden'` and ellipsis lets long unbroken
 * option names shrink inside a narrow popover without pushing the trailing
 * check icon or secondary identifier past the clipped right edge.
 */
export const comboboxItemLabel = style({
  flex: '0 1 auto',
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const comboboxItemHighlighted = style({
  backgroundColor: 'var(--muted)',
  outline: '1px solid var(--primary)',
  outlineOffset: '-1px',
})

/**
 * The selected row. A row that carries secondary text shows no check icon (the
 * icon and the secondary text compete for the same trailing slot), so this
 * weight is the only marker of the current selection there.
 */
export const comboboxItemSelected = style({
  fontWeight: 'var(--font-bold)',
})

/**
 * An item's secondary text (e.g. a language id beside its display name).
 * Right-aligned, muted, and monospace so it reads as an identifier and never
 * blends into the label.
 */
export const comboboxItemSecondary = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  marginLeft: 'auto',
  flexShrink: 0,
})
