import { globalStyle, style } from '@vanilla-extract/css'

// Tool use/result messages - document-style, no bubble
export const toolMessage = style({
  alignSelf: 'stretch',
  fontSize: 'var(--text-7)',
  lineHeight: 1.6,
})

// Tool use header: "» ToolName(...)"
export const toolUseHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})

// Tool result header: "« ToolName"
export const toolResultHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})

// Icon styling
export const toolUseIcon = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})

export const toolResultIcon = style({
  color: 'var(--success)',
  flexShrink: 0,
})

// Tool result content area (markdown)
export const toolResultContent = style({
  color: 'var(--foreground)',
})

// Tool result content as preformatted text (for Bash, Grep, Read output)
export const toolResultContentPre = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
})

// Tool result error message (subtle styling for transient, auto-recovered errors)
export const toolResultError = style({
  color: 'var(--muted-foreground)',
})

// Tool result content with ANSI escape sequence rendering (for Bash output)
export const toolResultContentAnsi = style({
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
})

// Override Shiki's default <pre> styling inside ANSI tool result
globalStyle(`${toolResultContentAnsi} pre.shiki`, {
  margin: 0,
  padding: 0,
  border: 'none',
  background: 'none',
  backgroundColor: 'transparent',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  fontSize: 'inherit',
  fontFamily: 'inherit',
  lineHeight: 'inherit',
})

globalStyle(`${toolResultContentAnsi} pre.shiki code`, {
  padding: 0,
  background: 'none',
  backgroundColor: 'transparent',
  fontSize: 'inherit',
  fontFamily: 'inherit',
})

// Shiki dual-theme support for ANSI-rendered spans
globalStyle(`${toolResultContentAnsi} pre.shiki span`, {
  color: 'var(--shiki-light)',
  backgroundColor: 'var(--shiki-light-bg, transparent)',
})

globalStyle(`html[data-theme="dark"] ${toolResultContentAnsi} pre.shiki span`, {
  color: 'var(--shiki-dark)',
  backgroundColor: 'var(--shiki-dark-bg, transparent)',
})

// Prompt label shown above WebFetch tool result
export const toolResultPrompt = style({
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-1)',
})

// Tool sub-detail line (monospace, aligned with description: icon 16px + gap 4px = 20px indent)
const toolInputSubDetailBase = {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none' as const,
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  paddingLeft: '20px',
}

export const toolInputSubDetail = style({
  ...toolInputSubDetailBase,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const toolInputSubDetailExpanded = style({
  ...toolInputSubDetailBase,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
})

// Tool input detail text (natural language: descriptions, URLs, queries)
export const toolInputText = style({
  color: 'var(--foreground)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

// Tool input code text (commands, patterns — monospaced)
export const toolInputCode = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

// Diff stat: added lines count (green)
export const toolInputStatAdded = style({
  color: 'var(--success)',
})

// Diff stat: removed lines count (red)
export const toolInputStatRemoved = style({
  color: 'var(--danger)',
})

// File path display in tool messages
export const toolInputPath = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

// Tool header actions area (right-aligned group for Code + ThreadExpander buttons)
export const toolHeaderActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px',
  opacity: 0,
  transition: 'opacity 0.15s',
})

// Timestamp text in tool header actions (muted, small)
export const toolHeaderTimestamp = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
  userSelect: 'none',
  lineHeight: 1,
})

// Inside tool-use headers, right-align the actions area
globalStyle(`${toolUseHeader} .${toolHeaderActions}`, {
  marginLeft: 'auto',
})

// Inline control response tag in tool header (approved/rejected indicator)
export const controlResponseTag = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  flexShrink: 0,
})

// Body content area for tool_use renderers (expand-gated body below header)
export const toolBodyContent = style({
  marginLeft: '6px',
  paddingLeft: 'var(--space-3)',
  paddingRight: 'var(--space-3)',
  borderLeft: '2px solid var(--border)',
})

// AskUserQuestion: question item container
export const questionItem = style({
  paddingLeft: '20px',
  marginTop: 'var(--space-1)',
})

// AskUserQuestion: question header label (bold)
export const questionHeader = style({
  fontWeight: 600,
  color: 'var(--foreground)',
})

// AskUserQuestion: question text
export const questionText = style({
  color: 'var(--muted-foreground)',
})

// AskUserQuestion: answer text
export const answerText = style({
  color: 'var(--foreground)',
  marginTop: '2px',
})
