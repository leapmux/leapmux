/** The indent that keeps a continuation line inside its own bullet. */
const CONTINUATION_INDENT = '  '

/**
 * Build a Markdown bullet list from entries that may each hold several blocks.
 *
 * Every line after an entry's first is indented by two spaces, which is what keeps a
 * nested list, a block quote or a second paragraph INSIDE its own item. Prefixing the
 * first line alone breaks each of those out to the top level, so a summary of three
 * points rendered as one list of nine.
 *
 * A blank line stays blank: indenting it would add trailing whitespace that some
 * Markdown parsers read as a hard line break.
 */
export function markdownBulletList(entries: readonly string[]): string {
  return entries
    .map(entry => entry
      .split('\n')
      .map((line, index) => index === 0 ? `- ${line}` : line === '' ? '' : `${CONTINUATION_INDENT}${line}`)
      .join('\n'))
    .join('\n')
}
