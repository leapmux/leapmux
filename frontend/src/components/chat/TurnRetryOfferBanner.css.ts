import { style } from '@vanilla-extract/css'
import { compactActionLabelled, compactActionSize, compactTextSize } from '~/styles/tokens'

/**
 * The turn-retry offer, directly above the queue and the composer.
 *
 * The composer supplies the background and the gaps between its children.
 * See `inputArea` in `~/components/chat/ChatView.css.ts`.
 * The notice and its actions wrap according to the composer width.
 */
export const banner = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 0,
  fontSize: compactTextSize,
  color: 'var(--muted-foreground)',
})

/** Keep the icon beside a readable sentence when the actions require another row. */
export const notice = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  flex: '1 1 20rem',
  minWidth: 0,
})

/** Let the controls wrap inside a narrow tile or floating window. */
export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-2)',
  marginLeft: 'auto',
  minWidth: 0,
  maxWidth: '100%',
})

/** Takes the free width, so the actions stay at the end of the row. */
export const text = style({
  flex: 1,
  minWidth: 0,
})

/**
 * Use the shared compact control size as a minimum.
 * A long model label can wrap without clipping or increasing the composer width.
 */
export const action = style({
  ...compactActionLabelled,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: 'auto',
  minHeight: compactActionSize,
  minWidth: 0,
  maxWidth: '100%',
  whiteSpace: 'normal',
  overflowWrap: 'anywhere',
})
