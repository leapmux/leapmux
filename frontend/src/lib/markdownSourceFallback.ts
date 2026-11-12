import type { Root } from 'hast'
import rehypeStringify from 'rehype-stringify'
import { unified } from 'unified'

/**
 * The class of the `<pre>` that shows Markdown source as plain text. A plain
 * string, not a vanilla-extract name, because the worker builds the markup and
 * `markdownContent.css.ts` styles it.
 */
export const MARKDOWN_SOURCE_FALLBACK_CLASS = 'markdown-source-fallback'

/** The data attribute on the same `<pre>`: a stable hook for tests and the DOM. */
export const MARKDOWN_SOURCE_FALLBACK_ATTRIBUTE = 'data-markdown-source-fallback'

/** The sentence under the source, which tells the reader why it has no formatting. */
export const MARKDOWN_SOURCE_FALLBACK_NOTICE = 'This Markdown could not be rendered, so it shows as plain text.'

// The stringifier that the Markdown pipeline ends with, so this display escapes
// exactly as a rendered body does and needs no escaper of its own.
const stringifier = unified().use(rehypeStringify)

/**
 * Build the HTML that shows `text` as escaped plain text, followed by the notice.
 *
 * This is the last display of a Markdown body, never a size limit. A body reaches
 * it only when no Markdown render exists for it:
 * - The parser itself fails. micromark recurses once per nesting level, so about
 *   2,400 nested `>` markers on one line overflow V8's stack.
 * - The render worker crashes or cannot start, so no rendered HTML ever arrives.
 * - The render runs past its time budget: deeply nested input can keep the parser
 *   busy for minutes before it overflows, so the worker is stopped first.
 *
 * The browser never parses Markdown on the main thread, so this display must not
 * parse either: it states the source as one text node, which the stringifier
 * escapes.
 */
export function markdownSourceFallbackHtml(text: string): string {
  const tree: Root = {
    type: 'root',
    children: [
      {
        type: 'element',
        tagName: 'pre',
        properties: {
          className: [MARKDOWN_SOURCE_FALLBACK_CLASS],
          dataMarkdownSourceFallback: '',
        },
        children: [
          { type: 'text', value: text },
        ],
      },
      {
        type: 'element',
        tagName: 'p',
        properties: {},
        children: [
          { type: 'text', value: MARKDOWN_SOURCE_FALLBACK_NOTICE },
        ],
      },
    ],
  }
  return String(stringifier.stringify(tree))
}
