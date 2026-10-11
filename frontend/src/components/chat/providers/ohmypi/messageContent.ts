import { OH_MY_PI_ROLE } from '~/generated/contracts/ohmypi-protocol'
import { getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject } from '~/lib/jsonPick'

/**
 * The text blocks of a `message_end` frame, joined into paragraphs.
 *
 * omp's message mirrors the Anthropic block shape:
 * `{message: {role, content: [{type:'text', text} | {type:'thinking', thinking} | ...]}}`.
 * A `custom` message -- a notice omp injects for the model -- may carry its content as
 * one plain string instead.
 *
 * The thinking blocks are left out on purpose. The worker persists the thinking of an
 * assistant message as a reasoning row of its own, before the message's row, so the
 * message's row draws its text alone.
 */
export function ohMyPiContentText(parent: Record<string, unknown>): string {
  const message = pickObject(parent, 'message')
  if (message?.role === OH_MY_PI_ROLE.Custom && typeof message.content === 'string')
    return message.content
  return joinContentParagraphs(getMessageContent(parent), { text: 'text' })
}
