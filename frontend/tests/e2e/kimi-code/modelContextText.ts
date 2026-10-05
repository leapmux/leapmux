import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * The raw text of one native Kimi Code model request.
 *
 * The generic `nativeModelContextText` JSON-stringifies the body, so a native
 * reminder that quotes its mode (`You are now in "agent swarm" mode.`) never
 * matches a literal substring: the quotes arrive escaped. This reader joins the
 * actual message text instead. An assistant step of tool calls alone carries a
 * null content and contributes nothing.
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
