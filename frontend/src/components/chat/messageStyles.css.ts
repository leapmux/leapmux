import { globalStyle, style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'
import { toolHeaderActions, toolHeaderButtonHidden } from './toolStyles.css'

export const messageBubble = style({
  position: 'relative',
  padding: `${spacing.md} ${spacing.lg}`,
  borderRadius: 'var(--radius-medium)',
  lineHeight: 1.6,
  maxWidth: '85%',
  wordBreak: 'break-word',
})

export const userMessage = style([messageBubble, {
  backgroundColor: 'color-mix(in srgb, var(--primary) 6%, var(--card))',
  border: '1px solid var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-end',
}])

export const assistantMessage = style([messageBubble, {
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-start',
}])

export const systemMessage = style([messageBubble, {
  backgroundColor: 'transparent',
  border: '1px dashed var(--border)',
  color: 'var(--muted-foreground)',
  alignSelf: 'center',
  fontSize: 'var(--text-7)',
}])

export const metaMessage = style({
  alignSelf: 'stretch',
  minWidth: 0,
})

export const resultDivider = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  selectors: {
    '&::before': {
      content: '""',
      flex: 1,
      height: '1px',
      background: 'var(--border)',
    },
    '&::after': {
      content: '""',
      flex: 1,
      height: '1px',
      background: 'var(--border)',
    },
  },
})

// Control response message (compact, muted)
export const controlResponseMessage = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  alignSelf: 'stretch',
})

// Base styles for message row layout
const messageRowBase = {
  display: 'flex',
  alignItems: 'flex-start',
  gap: '4px',
  alignSelf: 'stretch',
  maxWidth: '100%',
} as const

// Flex row wrapping a message bubble + right-aligned ToolHeaderActions outside the bubble
export const messageRow = style(messageRowBase)

// Right-aligned variant for user message bubbles
export const messageRowEnd = style({
  ...messageRowBase,
  justifyContent: 'flex-end',
})

// Centered variant for status/notification messages
export const messageRowCenter = style({
  ...messageRowBase,
  justifyContent: 'center',
  position: 'relative',
})

// Extra vertical spacing for user, assistant, and notification message rows
globalStyle(`${messageRow}:has(> .${assistantMessage}), ${messageRowEnd}, ${messageRowCenter}`, {
  marginTop: spacing.xs,
  marginBottom: spacing.xs,
})

// Inside messageRow, stretch meta messages (tools, result dividers) to fill available space
globalStyle(`${messageRow} > .${metaMessage}`, {
  flex: 1,
  alignSelf: 'auto',
})

// Inside messageRow containing a resultDivider, position actions absolutely at the right edge
globalStyle(`${messageRow}:has(.${resultDivider})`, {
  position: 'relative',
})

globalStyle(`${messageRow}:has(.${resultDivider}) > .${toolHeaderActions}`, {
  position: 'absolute',
  right: 0,
  marginLeft: 0,
  paddingLeft: 0,
})

// Inside messageRowEnd, place actions to the left of the bubble
globalStyle(`${messageRowEnd} > .${toolHeaderActions}`, {
  order: -1,
  marginLeft: 0,
  paddingLeft: 0,
  paddingRight: spacing.xs,
})

// Inside messageRowCenter, position actions at the right edge absolutely
globalStyle(`${messageRowCenter} > .${toolHeaderActions}`, {
  position: 'absolute',
  right: 0,
  marginLeft: 0,
  paddingLeft: 0,
})

// When hovering a message row, reveal hidden toolbar buttons
globalStyle(`${messageRow}:hover .${toolHeaderButtonHidden}, ${messageRowEnd}:hover .${toolHeaderButtonHidden}, ${messageRowCenter}:hover .${toolHeaderButtonHidden}`, {
  opacity: 1,
})

globalStyle(`${messageBubble} code`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

globalStyle(`${messageBubble} pre`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})
