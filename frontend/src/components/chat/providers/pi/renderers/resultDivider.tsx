import type { ResultDividerModel } from '../../registry'
import { isObject, pickString } from '~/lib/jsonPick'
import { PI_EVENT } from '../protocol'

function lastAssistantMessage(messages: unknown): Record<string, unknown> | null {
  if (!Array.isArray(messages))
    return null
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (isObject(msg) && pickString(msg, 'role') === 'assistant')
      return msg
  }
  return null
}

/**
 * Pi `agent_end` → result_divider model, read from the last assistant message's
 * `stopReason`/`errorMessage`. Null when the message isn't an `agent_end`.
 */
export function piResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== PI_EVENT.AgentEnd)
    return null

  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant ? pickString(assistant, 'stopReason') : ''
  const errorMessage = assistant ? pickString(assistant, 'errorMessage') : ''

  if (stopReason === 'error')
    return { label: errorMessage ? `Turn failed — ${errorMessage}` : 'Turn failed', isError: true }
  if (stopReason === 'aborted')
    return { label: 'Turn aborted', isError: true }
  if (stopReason === 'length')
    return { label: 'Turn ended (length limit)' }
  return { label: 'Turn ended' }
}
