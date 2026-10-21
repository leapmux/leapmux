import { PI_ENTRY_TYPE, PI_ROLE } from '~/generated/contracts/pi-protocol'
import { asContentArray, joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject } from '~/lib/jsonPick'
import { piSessionEntry } from './sessionEntry'

/** Read visible text from a custom session entry or a message. Keep reasoning in its own row. */
export function piContentText(parent: Record<string, unknown>): string {
  const entry = piSessionEntry(parent)
  const message = entry?.type === PI_ENTRY_TYPE.CustomMessage ? entry : pickObject(parent, 'message')
  if (!message)
    return ''
  if ((entry === message || message.role === PI_ROLE.Custom) && typeof message.content === 'string')
    return message.content
  return joinContentParagraphs(asContentArray(message.content), { text: 'text' })
}
