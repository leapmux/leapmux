import { style } from '@vanilla-extract/css'

/** The statement that fills the diff pane when the diff finds no change. */
export const equalVersionsNotice = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  boxSizing: 'border-box',
  padding: 'var(--space-4)',
  textAlign: 'center',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})

/**
 * The diff while its lines mount: invisible and without height, but still laid
 * out. Each slice then lays out its own lines, and the reveal lays out nothing
 * new. `display: none` would leave the layout of every line to the reveal, in
 * one long task.
 */
export const versionDiffMounting = style({
  visibility: 'hidden',
  height: 0,
  overflow: 'hidden',
})
