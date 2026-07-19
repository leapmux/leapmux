import { globalStyle, style } from '@vanilla-extract/css'
import {
  BLOCKED_IMAGE_CHIP_CLASS,
  BLOCKED_IMAGE_CLASS,
  BLOCKED_IMAGE_LABEL_CLASS,
} from '~/lib/rehypeBlockRemoteImages'
import { codeBlockCode, codeBlockPre, codeWrap } from '~/styles/codeBlock'
import { iconSize } from '~/styles/tokens'
import { shikiDualThemeColors } from '../shikiTokenColors.css'

export const markdownContent = style({
  wordBreak: 'break-word',
})

// Code blocks: move scroll to <code> so the copy button stays fixed.
globalStyle(`${markdownContent} pre`, codeBlockPre('hidden'))
globalStyle(`${markdownContent} pre code`, codeBlockCode)
// Rendered (read-only) markdown code blocks WRAP long lines like every other read-only
// code surface (tool output, Read, diff) instead of scrolling horizontally, which is
// awkward inside a chat message; the copy button preserves the exact source regardless.
// Scoped to markdownContent so the Milkdown EDITOR (which shares codeBlockCode) keeps
// horizontal scroll for a stable caret while typing.
globalStyle(`${markdownContent} pre code`, codeWrap)

// Shiki dual-theme support via CSS variables (color only -- the wrapper owns the bg)
shikiDualThemeColors(`${markdownContent} pre.shiki`)
shikiDualThemeColors(`${markdownContent} pre.shiki span`)

// Task list checkboxes
globalStyle(`${markdownContent} li > input[type="checkbox"]`, {
  marginRight: 'var(--space-1)',
  verticalAlign: 'middle',
  pointerEvents: 'none',
})

// Copy button for code blocks (injected via DOM by MessageBubble.injectCopyButtons).
//
// Keyed to the `code-copy-host` marker class the injector adds to every <pre> it
// augments -- NOT to `.markdownContent`. The button is injected into code blocks in any
// context (markdown bodies AND non-markdown <pre> such as a result-divider error
// detail), but the positioning used to be scoped to `${markdownContent} pre ...`, so a
// <pre> outside the markdown wrapper got an UNpositioned button that fell inline at the
// end of the text. Anchoring on the marker class instead positions it top-right
// everywhere, and the marker carries `position: relative` so the absolute button anchors
// to its own <pre> regardless of the surrounding layout.
export const codeCopyHostClass = 'code-copy-host'

globalStyle(`.${codeCopyHostClass}`, {
  position: 'relative',
})

globalStyle(`.${codeCopyHostClass} .copy-code-button`, {
  all: 'unset',
  boxSizing: 'border-box',
  position: 'absolute',
  top: 'var(--space-1)',
  right: 'var(--space-1)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: iconSize.container.md,
  height: iconSize.container.md,
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  backgroundColor: 'var(--card)',
  color: 'var(--muted-foreground)',
  cursor: 'pointer',
  opacity: '0',
  transition: 'opacity var(--transition)',
})

globalStyle(`.${codeCopyHostClass}:hover .copy-code-button`, {
  opacity: '1',
})

globalStyle(`.${codeCopyHostClass} .copy-code-button:hover`, {
  backgroundColor: 'var(--card)',
  color: 'var(--foreground)',
})

// Placeholder for an image the markdown pipeline refused to fetch (see
// rehypeBlockRemoteImages: only `data:`/`blob:` images render, because a remote image URL
// exfiltrates conversation content and the user's IP with no click).
//
// Keyed to the marker classes the rehype plugin emits -- NOT scoped under
// `.markdownContent` -- for the same reason `code-copy-host` is: the rendered HTML is
// injected in several contexts and the placeholder must look intentional in all of them.
// The class names live in the plugin module rather than here because that module is
// bundled into the markdown Worker, which must not import a `.css.ts`.
globalStyle(`.${BLOCKED_IMAGE_CLASS}`, {
  display: 'inline-flex',
  alignItems: 'baseline',
  gap: 'var(--space-2)',
  maxWidth: '100%',
  padding: 'var(--space-1) var(--space-2)',
  borderRadius: 'var(--radius-small)',
  border: '1px dashed var(--border)',
  backgroundColor: 'var(--card)',
  verticalAlign: 'baseline',
})

globalStyle(`.${BLOCKED_IMAGE_CHIP_CLASS}`, {
  flexShrink: 0,
  fontSize: 'var(--text-8)',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
})

// The author's alt text (or the URL when there is no alt). Clickable only when the
// sibling link-hardening pass kept it an <a> (http(s) srcs); otherwise it is plain text.
globalStyle(`.${BLOCKED_IMAGE_LABEL_CLASS}`, {
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  color: 'var(--muted-foreground)',
})
