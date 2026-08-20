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

/**
 * Styles for `<pre>`: reset padding, set position, configure overflow, and round
 * the block's corners.
 *
 * NO BORDER, deliberately. A fenced block is marked by its field alone --
 * `--code-block-background`, a translucent step over whatever hosts it (see
 * ~/styles/codePalette). That is a quiet block: on the tightest palette in the
 * catalogue, inside a user message's accent bubble, the step measures 1.07:1.
 * An outline was tried and removed as too loud for what a run of code in a
 * message should be. The radius stays, because it is what shapes the field.
 *
 * The radius is Oat's own for a `<pre>` -- `--radius-medium`, three times the
 * `--radius-small` it gives INLINE `code`. This restated it as `small` and so
 * rounded a block at the inline corner. Oat's rule is in its `base` layer, and
 * `codeBlockPre` is applied through an unlayered class, so the value here always
 * wins and had to be the right one. `codeBlock.test.ts` reads Oat's stylesheet
 * and pins the two together.
 *
 * The diff container and the Read view still outline themselves. They are not
 * fenced blocks -- they draw their own rows and need an edge to draw them
 * against.
 */
export function codeBlockPre(overflowX: 'hidden' | 'visible'): StyleRule {
  return {
    position: 'relative',
    overflowX,
    padding: 0,
    borderRadius: 'var(--radius-medium)',
  }
}

/** Styles for `<pre> code`: shared code typography, block display, scroll, and padding. */
export const codeBlockCode: StyleRule = {
  ...codeTypography,
  display: 'block',
  overflowX: 'auto',
  padding: 'var(--space-4)',
}
