import { style } from '@vanilla-extract/css'

/**
 * The two labelled rows of `WorkingTree.tsx`: a `max-content` label column and
 * a value column that takes the rest.
 *
 * `minmax(0, 1fr)` rather than `1fr` on the value column, because a grid track
 * has an `auto` minimum by default: a long absolute path would then set the
 * track's floor to its own unbroken width and push the whole block past the
 * tooltip's `max-width` instead of wrapping inside it.
 */
export const rows = style({
  display: 'grid',
  gridTemplateColumns: 'max-content minmax(0, 1fr)',
  columnGap: 'var(--space-3)',
  rowGap: 'var(--space-1)',
  alignItems: 'baseline',
})

/**
 * NO `font-size` in this file. The block INHERITS its container's type, because
 * it is a tooltip body at three call sites and dialog body content at three
 * others. A tooltip sets `--text-8` on its own surface (see
 * `~/components/common/Tooltip.css.ts`) and a dialog gets `--text-regular` from
 * Oat's `body, dialog, [popover]` base rule -- so stating a size here rendered
 * the rows at 12px directly above 16px status lines in every dialog.
 */
export const label = style({
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
})

/** The kind row's value: icon, name, and the diff badge when there is one. */
export const kindValue = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--foreground)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

/**
 * The branch name itself. Monospace, like the path below it and like the
 * `<code>` the dialogs' old status lines wrapped it in: a branch name is an
 * identifier, and `l`/`1`/`I` must not read alike in a delete confirmation.
 */
export const nameValue = style({
  fontFamily: 'var(--font-mono)',
})

/**
 * The directory row's value. Monospace, like every other path this app shows
 * (see `infoValue` in `~/components/chat/ChatView.css.ts`), and wrapping at any
 * character so a deep path folds inside the tooltip rather than widening it.
 */
export const pathValue = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})
