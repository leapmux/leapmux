import type { MockModelRequestRecord, MockModelRule, MockModelStep } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

/** Select the automatic continuation caused by a native Plan-to-Act change. */
export function diracActModeContinuationRule(name: string, respond: MockModelStep): MockModelRule {
  return {
    name,
    when: { user: String.raw`^<environment_details>\nThe user switched to ACT MODE\. You are now in Act mode\.\n\n# Current Mode\nACT MODE(?:\n|$)` },
    respond,
    once: true,
  }
}

/** Read the latest explicit native mode, including a pending Plan's feedback turns. */
export function diracCurrentModeDetails(request: MockModelRequestRecord): string {
  if (request.protocol !== 'openai-chat-completions')
    throw new Error('The Dirac mode scenario requires its OpenAI-compatible request.')
  const body = isObject(request.body)
    ? request.body
    : null
  const messages = Array.isArray(body?.messages)
    ? body.messages
    : []
  const texts = messages.filter(isObject).filter(message => message.role === 'user').flatMap((message) => {
    if (typeof message.content === 'string')
      return [message.content]
    if (!Array.isArray(message.content))
      return []
    return message.content.filter(isObject).flatMap(part => typeof part.text === 'string'
      ? [part.text]
      : [])
  })
  return texts.findLast(text => text.startsWith('<environment_details>\n')
    && text.endsWith('\n</environment_details>')
    && /\n# Current Mode\n(?:PLAN|ACT) MODE(?:\n|$)/u.test(text)) ?? ''
}
