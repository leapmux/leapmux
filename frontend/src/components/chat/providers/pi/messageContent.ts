import { getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'

type Kind = 'text' | 'thinking'

/**
 * Join text from `parent.message.content[]` blocks matching `kind` into a
 * paragraph-separated string (≥2 newlines between blocks). Pi's
 * `message_end` envelope mirrors Anthropic's content-block shape:
 * `{ message: { content: [{type:'text', text}|{type:'thinking', thinking}] } }`.
 *
 * Image blocks (Pi's `read` on a binary image file) are embedded as
 * Markdown via the helper's default formatter — Pi's assistant message
 * renderer feeds this string to MarkdownText, so they render inline.
 */
export function piContentText(parent: Record<string, unknown>, kind: Kind): string {
  return joinContentParagraphs(getMessageContent(parent), { [kind]: kind })
}

/**
 * True when `parent.message.content[]` carries at least one non-empty
 * thinking block but no non-empty text blocks — used to route a message_end
 * to the thinking renderer instead of the assistant-text renderer.
 *
 * Signature-only thinking blocks (`thinking: ''` plus a signature) can be
 * emitted next to tool-call blocks. They have no visible content, so they
 * must not create an empty "Thinking" bubble while the tool call renders
 * through its own event.
 */
export function piIsThinkingOnly(parent: Record<string, unknown>): boolean {
  const content = getMessageContent(parent)
  if (!content)
    return false
  let sawThinking = false
  for (const block of content) {
    if (!isObject(block))
      continue
    if (block.type === 'thinking') {
      if (typeof block.thinking === 'string' && block.thinking.trim() !== '')
        sawThinking = true
    }
    else if (block.type === 'text' && typeof block.text === 'string' && block.text.trim() !== '') {
      return false
    }
  }
  return sawThinking
}
