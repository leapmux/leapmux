import { GROK_METHOD, GROK_NOTIFICATION } from '~/generated/contracts/grok-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * The stop reason of a turn that Grok started by itself, or undefined for any other
 * frame.
 *
 * Grok runs a turn of its own for a goal or for a subagent that finished in the
 * background, and it ends every turn with `turn_completed` in its own session
 * notification. The worker stores that frame as the end of such a turn, as it stores
 * the prompt response of a turn LeapMux started. The notification spells the reason
 * `stop_reason`, where the protocol's prompt response spells it `stopReason`.
 *
 * A `turn_completed` that states no reason still ends the turn, so it answers the
 * empty reason, which the divider reads as a plain end.
 */
export function grokAgentTurnEnd(parent: Record<string, unknown>): string | undefined {
  if (parent.method !== GROK_METHOD.SessionNotification)
    return undefined
  const update = pickObject(pickObject(parent, 'params'), 'update')
  if (pickString(update, 'sessionUpdate') !== GROK_NOTIFICATION.TurnCompleted)
    return undefined
  const reason = update?.stop_reason
  return typeof reason === 'string' ? reason : ''
}
