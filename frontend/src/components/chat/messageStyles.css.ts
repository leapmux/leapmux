import { globalStyle, style } from '@vanilla-extract/css'
import { toolHeaderActions, toolHeaderTimestamp } from './toolStyles.css'

export const messageBubble = style({
  position: 'relative',
  padding: 'var(--space-3) var(--space-4)',
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

export const thinkingMessage = style([messageBubble, {
  backgroundColor: 'var(--card)',
  border: '1px dashed var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-start',
}])

export const thinkingHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
  cursor: 'pointer',
  userSelect: 'none',
})

export const thinkingChevron = style({
  'flexShrink': 0,
  'transition': 'transform 150ms cubic-bezier(0.4, 0, 0.2, 1)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const thinkingChevronExpanded = style({
  transform: 'rotate(90deg)',
})

export const thinkingContent = style({
  marginTop: 'var(--space-2)',
})

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
  gap: 'var(--space-2)',
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

// Control response message (compact)
export const controlResponseMessage = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  color: 'var(--foreground)',
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
globalStyle(`${messageRow}:has(> .${assistantMessage}), ${messageRow}:has(> .${thinkingMessage}), ${messageRowEnd}, ${messageRowCenter}`, {
  marginTop: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
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
})

// Inside messageRowEnd, place actions to the left of the bubble in a 2-column grid (mirrored via RTL)
globalStyle(`${messageRowEnd} > .${toolHeaderActions}`, {
  order: -1,
  paddingRight: 'var(--space-1)',
  paddingTop: 'var(--space-1)',
  paddingBottom: 'var(--space-1)',
  display: 'grid',
  gridTemplateColumns: 'auto auto',
  direction: 'rtl',
})

// Reset direction on children so text inside buttons renders LTR
globalStyle(`${messageRowEnd} > .${toolHeaderActions} > *`, {
  direction: 'ltr',
})

// 2-column grid layout for assistant/thinking bubble actions so primary actions sit adjacent to the bubble
globalStyle(`${messageRow}:has(> .${assistantMessage}) > .${toolHeaderActions}, ${messageRow}:has(> .${thinkingMessage}) > .${toolHeaderActions}`, {
  paddingTop: 'var(--space-1)',
  paddingBottom: 'var(--space-1)',
  display: 'grid',
  gridTemplateColumns: 'auto auto',
})

// Add left padding to timestamps in assistant grid so they align with the icon button below
globalStyle(`${messageRow}:has(> .${assistantMessage}) > .${toolHeaderActions} .${toolHeaderTimestamp}, ${messageRow}:has(> .${thinkingMessage}) > .${toolHeaderActions} .${toolHeaderTimestamp}`, {
  paddingLeft: 'var(--space-1)',
})

// Add right padding to timestamps in user grid (mirrored) so they align with the icon button below
globalStyle(`${messageRowEnd} > .${toolHeaderActions} .${toolHeaderTimestamp}`, {
  paddingRight: 'var(--space-1)',
})

// Inside messageRowCenter, position actions at the right edge absolutely
globalStyle(`${messageRowCenter} > .${toolHeaderActions}`, {
  position: 'absolute',
  right: 0,
  marginLeft: 0,
})

// When hovering a message row, reveal the actions
globalStyle(`${messageRow}:hover .${toolHeaderActions}, ${messageRowEnd}:hover .${toolHeaderActions}, ${messageRowCenter}:hover .${toolHeaderActions}`, {
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
