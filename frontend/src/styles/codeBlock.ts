import type { StyleRule } from '@vanilla-extract/css'

/**
 * Shared code block styles: move horizontal scroll from `<pre>` (set by Oat)
 * to `<code>` so that absolutely-positioned overlays (copy button, language
 * label) stay fixed. Padding is also moved to `<code>` so the scrollbar
 * sits at the `<pre>` border edge.
 *
 * Usage in a `.css.ts` file:
 * ```ts
 * globalStyle(`${parent} pre`, codeBlockPre('hidden'))
 * globalStyle(`${parent} pre code`, codeBlockCode)
 * ```
 */

/**
 * The shared code typography contract: one monospace family, ligature setting, size, and
 * line height for EVERY code surface -- markdown/editor fenced blocks, the Read view,
 * diffs, tool output, the command body/summary, and the raw-JSON block. Spread it into
 * each so a size or line-height change lands everywhere at once and the surfaces can't
 * drift (previously each duplicated these four values, and fenced blocks inherited the
 * prose context instead -- larger, at a looser 1.6 line height than the rest).
 */
export const codeTypography: StyleRule = {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
}

/**
 * Line-wrapping for code surfaces that WRAP rather than scroll: preserve whitespace but
 * let long unbroken tokens break so they don't force horizontal overflow. Used by the tool
 * output / command body+summary, the raw-JSON block, and the Read/diff line rows. NOT the
 * fenced code blocks (`codeBlockCode`), which scroll horizontally (white-space: pre), so
 * this stays separate from {@link codeTypography} rather than folded into it.
 */
export const codeWrap: StyleRule = {
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
}

/** Styles for `<pre>`: reset padding, set position, and configure overflow. */
export function codeBlockPre(overflowX: 'hidden' | 'visible'): StyleRule {
  return {
    position: 'relative',
    overflowX,
    padding: 0,
  }
}

/** Styles for `<pre> code`: shared code typography, block display, scroll, and padding. */
export const codeBlockCode: StyleRule = {
  ...codeTypography,
  display: 'block',
  overflowX: 'auto',
  padding: 'var(--space-4)',
}
