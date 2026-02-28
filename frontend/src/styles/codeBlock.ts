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

/** Styles for `<pre>`: reset padding, set position, and configure overflow. */
export function codeBlockPre(overflowX: 'hidden' | 'visible'): StyleRule {
  return {
    position: 'relative',
    overflowX,
    padding: 0,
  }
}

/** Styles for `<pre> code`: block display, horizontal scroll, and padding. */
export const codeBlockCode: StyleRule = {
  display: 'block',
  overflowX: 'auto',
  padding: 'var(--space-4)',
}
