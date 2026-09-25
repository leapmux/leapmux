import type { TurnEnd } from '../../../model/divider'
import { MIMO_EVENT, MIMO_STATUS_TYPE } from '~/generated/contracts/mimo-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { MIMO_ERROR_NAME } from '../protocol'
import { mimoErrorText } from './notification'
import { mimoEvent } from './toolCommon'

/**
 * A MiMo turn end as the provider-neutral divider model.
 *
 * The worker persists the event that ENDED the turn: the `session.status` idle of a
 * turn that finished or that an abort stopped, or the `session.error` of a turn that
 * failed. The idle event states no outcome of its own, so LeapMux's completion column
 * says whether the reader stopped the turn. Returns null for any other row.
 */
export function mimoResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  const event = mimoEvent(parsed)
  if (!event)
    return null
  if (event.type === MIMO_EVENT.SessionStatus) {
    if (pickString(pickObject(event.properties, 'status'), 'type') !== MIMO_STATUS_TYPE.Idle)
      return null
    if (completion === MessageCompletion.INTERRUPTED)
      return { label: turnEndLabel('interrupted') }
    return { label: turnEndLabel('ended') }
  }
  if (event.type === MIMO_EVENT.SessionError) {
    const { name, message } = mimoErrorText(event.properties)
    if (name === MIMO_ERROR_NAME.Aborted || completion === MessageCompletion.INTERRUPTED)
      return { label: turnEndLabel('interrupted') }
    return { label: turnEndLabel('failed', { qualifiers: [name], reason: message }), isError: true }
  }
  return null
}
