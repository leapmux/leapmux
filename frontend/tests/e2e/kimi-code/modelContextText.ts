import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * The message text of one native Kimi Code model request.
 *
 * The generic `nativeModelContextText` reads every string of the body, tool
 * schemas included, so a negative check of a mode word can match a schema. This
 * reader joins the message text alone, and it refuses a message whose content is
 * not text. An assistant step of tool calls alone carries a null content and
 * contributes nothing.
 */
export function kimiModelContextText(request: MockModelRequestRecord): string {
  if (request.protocol !== 'openai-chat-completions')
    throw new Error('The Kimi Code context reader requires its native OpenAI chat completions request.')
  if (!isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The Kimi Code model context must contain a native message array.')
  const text: string[] = []
  for (const message of request.body.messages) {
    if (!isObject(message) || typeof message.role !== 'string')
      throw new Error('The Kimi Code model context contains an invalid native message.')
    if (message.content === null || message.content === undefined)
      continue
    if (typeof message.content !== 'string')
      throw new Error('The Kimi Code model context contains a non-text native message.')
    text.push(message.content)
  }
  return text.filter(part => part !== '').join('\n')
}
