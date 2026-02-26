import { globalStyle, style } from '@vanilla-extract/css'
import { iconSize, spacing } from '~/styles/tokens'

export const headingPreviewItem = style({
  margin: 0,
})

export const headingPickerButton = style({
  width: 'auto',
  height: iconSize.container.md,
  gap: '2px',
  padding: '0 4px',
})

export const iconNudge = style({
  marginTop: '1px',
})

export const container = style({
  position: 'relative',
  display: 'flex',
  flexDirection: 'column',
  width: '100%',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  backgroundColor: 'var(--background)',
  overflow: 'hidden',
  selectors: {
    '&:focus-within': {
      borderColor: 'var(--ring)',
    },
  },
})

export const toolbar = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.xs,
  padding: `${spacing.xs} ${spacing.sm}`,
  borderBottom: '1px solid var(--border)',
  backgroundColor: 'var(--card)',
  flexShrink: 0,
  flexWrap: 'wrap',
  minHeight: iconSize.container.md,
})

// Make ot-dropdown participate in flex alignment
globalStyle(`${toolbar} > ot-dropdown`, {
  display: 'inline-flex',
  alignItems: 'center',
})

// Style <hr> separators inside the toolbar
globalStyle(`${toolbar} > hr`, {
  all: 'unset',
  width: '1px',
  height: '14px',
  backgroundColor: 'var(--border)',
  flexShrink: 0,
})

export const enterModeWrapper = style({
  marginLeft: 'auto',
  display: 'inline-flex',
})

// Override OAT .ghost styles to match the muted style of IconButton toolbar items.
globalStyle(`${enterModeWrapper} > button`, {
  color: 'var(--muted-foreground)',
  padding: 0,
})

globalStyle(`${enterModeWrapper} > button:hover`, {
  color: 'var(--foreground)',
  backgroundColor: 'transparent',
})

export const linkPopover = style({
  position: 'fixed',
  margin: 0,
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  padding: spacing.xs,
  boxShadow: 'var(--shadow-large)',
  // Opacity animation matching OAT dropdown pattern
  opacity: 0,
  transform: 'translateY(4px)',
  transition: 'opacity 150ms ease-out, transform 150ms ease-out, display 150ms allow-discrete, overlay 150ms allow-discrete',
  selectors: {
    '&:popover-open': {
      display: 'flex',
      opacity: 1,
      transform: 'translateY(0)',
    },
  },
})

export const linkPopoverForm = style({
  display: 'flex',
  gap: spacing.xs,
  alignItems: 'center',
})

export const linkPopoverInput = style({
  'all': 'unset',
  'fontSize': 'var(--text-7)',
  'padding': `${spacing.xs} ${spacing.sm}`,
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-small)',
  'backgroundColor': 'var(--background)',
  'color': 'var(--foreground)',
  'width': '220px',
  ':focus': {
    borderColor: 'var(--ring)',
  },
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

export const codeLangPopoverContent = style({
  display: 'flex',
  flexDirection: 'column',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-large)',
  width: '280px',
})

export const comboboxControl = style({
  display: 'flex',
  alignItems: 'center',
  padding: `${spacing.xs} ${spacing.sm}`,
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

export const comboboxListbox = style({
  maxHeight: '200px',
  overflowY: 'auto',
  padding: spacing.xs,
})

export const comboboxItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.xs,
  fontSize: 'var(--text-7)',
  padding: `${spacing.xs} ${spacing.sm}`,
  cursor: 'pointer',
  color: 'var(--foreground)',
  borderRadius: 'var(--radius-small)',
})

export const comboboxItemHighlighted = style({
  backgroundColor: 'var(--muted)',
  outline: '1px solid var(--primary)',
  outlineOffset: '-1px',
})

export const comboboxItemCode = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  marginLeft: 'auto',
  flexShrink: 0,
})

export const editorWrapper = style({
  minHeight: '38px',
  maxHeight: '50vh',
  overflowY: 'auto',
})

// ProseMirror editor layout
globalStyle(`${editorWrapper} .ProseMirror`, {
  padding: `${spacing.sm} ${spacing.md}`,
  outline: 'none',
  minHeight: '20px',
  whiteSpace: 'pre-wrap',
  wordWrap: 'break-word',
})

// Code blocks need relative positioning for the language label
globalStyle(`${editorWrapper} .ProseMirror pre`, {
  position: 'relative',
})

// Code block language label -- uses sticky + float to stay at top-right during horizontal scroll
globalStyle(`${editorWrapper} .ProseMirror pre .code-lang-label`, {
  position: 'sticky',
  float: 'right',
  right: '4px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  padding: '1px 4px',
  borderRadius: 'var(--radius-small)',
  userSelect: 'none',
  marginBottom: '-1.5em',
  zIndex: 1,
})

globalStyle(`${editorWrapper} .ProseMirror pre .code-lang-label:hover`, {
  backgroundColor: 'var(--card)',
  color: 'var(--muted-foreground)',
})

// Shiki syntax highlighting in editor code blocks (via prosemirror-highlight)
// Light theme: use --shiki-light CSS variables from inline decorations
globalStyle(`${editorWrapper} .ProseMirror pre .shiki`, {
  color: 'var(--shiki-light)',
})

// Dark theme: use --shiki-dark CSS variables
globalStyle(`html[data-theme="dark"] ${editorWrapper} .ProseMirror pre .shiki`, {
  color: 'var(--shiki-dark)',
})

// Task list checkboxes (ProseMirror-specific)
globalStyle(`${editorWrapper} .ProseMirror li[data-checked]`, {
  listStyle: 'none',
  position: 'relative',
  marginLeft: '-20px',
  paddingLeft: '20px',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked]::before`, {
  position: 'absolute',
  left: 0,
  top: '2px',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked="true"]::before`, {
  content: '"\\2611"',
  color: 'var(--primary)',
})

globalStyle(`${editorWrapper} .ProseMirror li[data-checked="false"]::before`, {
  content: '"\\2610"',
  color: 'var(--muted-foreground)',
})

globalStyle(`${editorWrapper} .ProseMirror .placeholder`, {
  color: 'var(--faint-foreground)',
  position: 'absolute',
  pointerEvents: 'none',
})

// Placeholder for empty editor
globalStyle(`${editorWrapper} .ProseMirror p.is-editor-empty:first-child::before`, {
  content: 'attr(data-placeholder)',
  color: 'var(--faint-foreground)',
  pointerEvents: 'none',
  float: 'left',
  height: 0,
})
