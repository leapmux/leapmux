/**
 * The keys that make the composer send `markdown` exactly, for Markdown of one-line paragraphs.
 *
 * The composer starts a new paragraph at each typed line break, and it sends a paragraph break as one blank line. A
 * typed blank line therefore adds an empty paragraph, which the composer sends as `<br />`. So the keys hold one line
 * break where `markdown` holds a blank line.
 *
 * Markdown with a line break inside a paragraph, or with an empty paragraph, has no such keys, and the function refuses
 * it: the composer sends neither from typed paragraphs.
 */
export function paragraphKeys(markdown: string): string {
  const paragraphs = markdown.split('\n\n')
  if (paragraphs.some(paragraph => paragraph === '' || paragraph.includes('\n')))
    throw new Error(`The composer cannot send ${JSON.stringify(markdown)} from typed paragraphs: a blank line must separate each two paragraphs, and each paragraph must be one line that is not empty.`)
  return paragraphs.join('\n')
}
