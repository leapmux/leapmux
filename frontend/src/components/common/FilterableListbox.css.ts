import { style } from '@vanilla-extract/css'

/**
 * Styles for the filterable listbox in `./settingsShared.tsx`.
 *
 * They live here rather than in `./markdownEditor/MarkdownEditor.css.ts` (their
 * first home) because three unrelated features render the widget: the composer's
 * settings chips, the composer's `[+]` submenus, and the code-language popover.
 * `FilterableListbox` applies them itself, so no caller passes them in and no
 * caller can forget one.
 */

export const comboboxControl = style({
  display: 'flex',
  alignItems: 'center',
  padding: 'var(--space-1) var(--space-2)',
  borderTop: '1px solid var(--border)',
})

export const comboboxInput = style({
  'all': 'unset',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'width': '100%',
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

/**
 * The filterable option list.
 *
 * `minHeight` matches `maxHeight` on purpose: the list must not resize while the
 * user types. Every popover that hosts it repositions on a content resize, so a
 * list that shrinks as the match count falls slides the panel out from under the
 * pointer mid-filter — and a filter that matches nothing collapses it to a bare
 * input. Holding one size costs some blank space on a short list and keeps the
 * panel still.
 */
const COMBOBOX_LISTBOX_HEIGHT = '200px'

export const comboboxListbox = style({
  minHeight: COMBOBOX_LISTBOX_HEIGHT,
  maxHeight: COMBOBOX_LISTBOX_HEIGHT,
  overflowY: 'auto',
  padding: 'var(--space-1)',
})

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
