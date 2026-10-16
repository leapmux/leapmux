import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeTextBlocks } from '../helpers/nativeScenario'

/**
 * The message text of one native Kimi Code model request.
 *
 * The generic `nativeModelContextText` reads every string of the body, tool
 * schemas included, so a negative check of a mode word can match a schema. This
 * reader joins message text alone and excludes image payloads. It rejects malformed content.
 * An assistant step with tool calls alone carries null content and contributes nothing.
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
    if (typeof message.content !== 'string' && !Array.isArray(message.content))
      throw new Error('The Kimi Code model context contains a non-text native message.')
    if (Array.isArray(message.content)) {
      for (const block of message.content) {
        if (!isObject(block) || (!(block.type === 'text' && typeof block.text === 'string')
          && !(block.type === 'image_url' && isObject(block.image_url) && typeof block.image_url.url === 'string'))) {
          throw new Error('The Kimi Code model context contains an invalid native content block.')
        }
      }
    }
    text.push(...nativeTextBlocks(message.content))
  }
  return text.filter(part => part !== '').join('\n')
}
