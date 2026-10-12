import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

function contentText(content: unknown, toolResult = false): string {
  if (typeof content === 'string')
    return content
  if (!Array.isArray(content))
    throw new Error('The DeepSeek Harness model content must contain native blocks or text.')
  return content.map((block: unknown) => {
    if (!isObject(block) || typeof block.type !== 'string')
      throw new Error('The DeepSeek Harness model content contains an invalid block.')
    if (block.type === 'text') {
      if (typeof block.text !== 'string')
        throw new Error('The DeepSeek Harness native text block has no text value.')
      return block.text
    }
    if (block.type === 'tool_result' && !toolResult)
      return contentText(block.content, true)
    if (block.type === 'thinking' && !toolResult) {
      if (typeof block.thinking !== 'string')
        throw new Error('The DeepSeek Harness native thinking block has no text value.')
      return block.thinking
    }
    if (block.type === 'image' || (!toolResult && (block.type === 'tool_use' || block.type === 'redacted_thinking')))
      return ''
    throw new Error('The DeepSeek Harness model content contains an unsupported native block.')
  }).join('')
}

export function deepseekHarnessModelContextText(request: MockModelRequestRecord): string {
  if (request.protocol !== 'anthropic-messages')
    throw new Error('The DeepSeek Harness context reader requires its native Anthropic Messages request.')
  if (!isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The DeepSeek Harness model context must contain a native message array.')
  const text: string[] = []
  if (request.body.system !== undefined)
    text.push(contentText(request.body.system))
  for (const message of request.body.messages) {
    if (!isObject(message) || (message.role !== 'user' && message.role !== 'assistant'))
      throw new Error('The DeepSeek Harness model context contains an invalid native message.')
    text.push(contentText(message.content))
  }
  return text.filter(part => part !== '').join('\n')
}
