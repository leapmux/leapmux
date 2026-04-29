import type { JSX } from 'solid-js'
import { isObject, pickString } from '~/lib/jsonPick'
import { resultDivider } from '../../../messageStyles.css'
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

/** Render Pi agent_end as the end-of-run result divider. */
export function renderPiResultDivider(parsed: unknown): JSX.Element | null {
  if (!isObject(parsed) || pickString(parsed, 'type') !== PI_EVENT.AgentEnd)
    return null

  const assistant = lastAssistantMessage(parsed.messages)
  const stopReason = assistant ? pickString(assistant, 'stopReason') : ''
  const errorMessage = assistant ? pickString(assistant, 'errorMessage') : ''

  if (stopReason === 'error') {
    return (
      <div class={resultDivider} style={{ color: 'var(--danger)' }}>
        {errorMessage ? `Turn failed — ${errorMessage}` : 'Turn failed'}
      </div>
    )
  }
  if (stopReason === 'aborted')
    return <div class={resultDivider} style={{ color: 'var(--danger)' }}>Turn aborted</div>
  if (stopReason === 'length')
    return <div class={resultDivider}>Turn ended (length limit)</div>
  return <div class={resultDivider}>Turn ended</div>
}
