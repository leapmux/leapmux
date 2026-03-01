import { globalStyle, style } from '@vanilla-extract/css'
import { todoList } from '~/components/todo/TodoList.css'

// Tool use/result messages - document-style, no bubble
export const toolMessage = style({
  alignSelf: 'stretch',
  fontSize: 'var(--text-7)',
  lineHeight: 1.6,
})

// Tool use header: "» ToolName(...)"
// Uses flex-start so multi-line titles keep icon + actions on the first line.
export const toolUseHeader = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})

// Icon styling — also used on the wrapper <span> so it acts as a flex-start-aligned
// line-height box, keeping the icon vertically centred on the first text line.
export const toolUseIcon = style({
  color: 'var(--muted-foreground)',
  height: '1lh',
  alignItems: 'center',
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

// Tool summary line (monospace)
const toolInputSummaryBase = {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none' as const,
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
}

export const toolInputSummary = style({
  ...toolInputSummaryBase,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const toolInputSummaryExpanded = style({
  ...toolInputSummaryBase,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
})

// Tool input detail text (natural language: descriptions, URLs, queries)
export const toolInputText = style({
  color: 'var(--foreground)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  minWidth: 0,
})

// Tool input code text (commands, patterns — monospaced)
export const toolInputCode = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  minWidth: 0,
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
  height: '1lh',
  gap: '2px',
  flexShrink: 0,
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

// TodoList inside tool body: remove horizontal padding (toolBodyContent already provides it)
globalStyle(`${toolBodyContent} > .${todoList}`, {
  paddingLeft: 0,
  paddingRight: 0,
})

// File list in Grep/Glob tool results
export const toolFileList = style({
  paddingLeft: '20px',
  margin: '4px 0',
  fontSize: 'var(--text-8)',
})

// AskUserQuestion: answer text
export const answerText = style({
  color: 'var(--foreground)',
  marginTop: '2px',
})
