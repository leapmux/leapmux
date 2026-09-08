import type { Nodes } from 'mdast'
import { createMarkdownParser } from './markdownParse'

/**
 * The markdown node types whose own `value` IS the text a reader sees.
 *
 * `html` is here on purpose, and it matches what the renderer does: the
 * pipeline shows a raw-HTML run as literal text rather than dropping it (see
 * `remarkHtmlAsText` in `./markdownProcessor.ts`), so a goal that says
 * `Replace <old-token>` must announce those characters too.
 */
const LITERAL_TYPES = new Set(['text', 'inlineCode', 'code', 'html'])

/**
 * A block whose end is a pause. Joining its siblings with a space would run two
 * sentences together, and a list would read as one long line.
 */
const BLOCK_TYPES = new Set([
  'paragraph',
  'heading',
  'listItem',
  'blockquote',
  'code',
  'tableRow',
  // Each cell too, or two adjacent cells read as one word ("a" + "b" = "ab").
  'tableCell',
  'thematicBreak',
])

function collect(node: Nodes, out: string[]): void {
  if (LITERAL_TYPES.has(node.type)) {
    out.push((node as { value: string }).value)
  }
  else if (node.type === 'image' || node.type === 'imageReference') {
    // The alt text is what the image contributes to a spoken reading.
    out.push(node.alt ?? '')
  }
  else if ('children' in node) {
    for (const child of node.children)
      collect(child as Nodes, out)
  }
  if (BLOCK_TYPES.has(node.type))
    out.push('\n')
}

/**
 * Reduce markdown SOURCE to the words a person reads.
 *
 * A screen reader given markdown source reads the syntax: "ship the asterisk
 * asterisk auth refactor asterisk asterisk". Anything that announces
 * model-written or user-written prose through `aria-live` has to strip the
 * marks first, because the visible surface renders them and the spoken one
 * cannot.
 *
 * It reuses `createMarkdownParser`, so what counts as a mark here is exactly
 * what the renderer treats as one -- the two cannot drift on, say, whether a
 * `~~strikethrough~~` is syntax.
 *
 * The result is a single line: every block boundary collapses to one space, so
 * the caller can put it inside a sentence.
 */
export function markdownToPlainText(markdown: string): string {
  if (markdown.trim() === '')
    return ''
  const out: string[] = []
  collect(createMarkdownParser().parse(markdown) as Nodes, out)
  return out.join('').replace(/\s+/g, ' ').trim()
}
