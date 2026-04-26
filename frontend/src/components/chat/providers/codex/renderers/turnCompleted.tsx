import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { resultDivider } from '../../../messageStyles.css'

/** Renders Codex turn/completed as a result divider. */
export function codexTurnCompletedRenderer(parsed: unknown, _role: MessageRole, _context?: RenderContext): JSX.Element | null {
  if (!isObject(parsed) || !isObject(parsed.turn))
    return null
  const turn = parsed.turn as Record<string, unknown>
  const status = (turn.status as string) || ''
  if (!status)
    return null

  // Failed turn: show error message from turn.error.message
  if (status === 'failed' && isObject(turn.error)) {
    const error = turn.error as Record<string, unknown>
    const message = pickString(error, 'message', 'Unknown error')
    const details = pickString(error, 'additionalDetails')
    const label = details ? `${message} — ${details}` : message
    return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{label}</div>
  }

  return <div class={resultDivider}>{`Turn ${status}`}</div>
}
