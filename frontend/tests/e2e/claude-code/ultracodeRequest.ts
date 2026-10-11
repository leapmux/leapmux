import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

/** Read native system text without assistant output, user text, or tool schemas. */
function systemText(content: unknown): string[] {
  if (typeof content === 'string')
    return [content]
  if (!Array.isArray(content))
    throw new Error('The native Claude system instruction has invalid content.')
  return content.flatMap((block: unknown) => {
    if (!isObject(block))
      throw new Error('The native Claude system instruction contains an invalid block.')
    if (block.type !== 'text')
      return []
    if (typeof block.text !== 'string')
      throw new Error('The native Claude system text block contains no text.')
    return [block.text]
  })
}

/** Read the last native Ultracode state instruction. Later token reminders do not change that state. */
export function claudeUltracodeEnabled(request: Pick<MockModelRequestRecord, 'protocol' | 'body'>): boolean {
  if (request.protocol !== 'anthropic-messages' || !isObject(request.body))
    throw new Error('The Ultracode proof requires an actual native Claude model request.')
  const parts: string[] = []
  if (request.body.system !== undefined)
    parts.push(...systemText(request.body.system))
  if (!Array.isArray(request.body.messages))
    throw new Error('The native Ultracode request contains no message array.')
  for (const message of request.body.messages) {
    if (!isObject(message))
      throw new Error('The native Ultracode request contains an invalid message.')
    if (message.role === 'system')
      parts.push(...systemText(message.content))
  }
  if (!parts.some(part => part !== ''))
    throw new Error('The native Ultracode request contains no system text.')
  let enabled = false
  for (const part of parts) {
    for (const match of part.matchAll(/(?:^|\n)Ultracode is (?:still )?(on|off)(?=[:\s]|$)/g))
      enabled = match[1] === 'on'
  }
  return enabled
}
