import { getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject } from '~/lib/jsonPick'

/**
 * Join the text blocks of `parent.message.content[]` into a paragraph-separated
 * string (≥2 newlines between blocks). Pi's `message_end` envelope mirrors
 * Anthropic's content-block shape:
 * `{ message: { content: [{type:'text', text}|{type:'thinking', thinking}] } }`.
 *
 * The thinking blocks stay out: the worker persists them as a reasoning row of
 * their own, before the message's row.
 *
 * Image blocks (Pi's `read` on a binary image file) are embedded as
 * Markdown via the helper's default formatter — Pi's assistant message
 * renderer feeds this string to MarkdownText, so they render inline.
 */
export function piContentText(parent: Record<string, unknown>): string {
  const message = pickObject(parent, 'message')
  if (message?.role === 'custom' && typeof message.content === 'string')
    return message.content
  return joinContentParagraphs(getMessageContent(parent), { text: 'text' })
}
