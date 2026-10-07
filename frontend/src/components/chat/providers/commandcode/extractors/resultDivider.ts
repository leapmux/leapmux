import type { TurnEnd } from '../../../model/divider'
import { COMMAND_CODE_METHOD } from '~/generated/contracts/commandcode-protocol'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { commandCodeError } from '../protocol'

/**
 * A Command Code `turn/completed` row read into the turn-end divider, or null for another row.
 *
 * The native frame states no duration. The Worker measures each turn and adds the duration to the turn end as its own
 * metadata, which the reader merges into the frame.
 */
export function commandCodeResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || parsed.method !== COMMAND_CODE_METHOD.TurnCompleted)
    return null
  const params = pickObject(parsed, 'params')
  const reason = pickString(params, 'stopReason')
  const durationMs = pickNumber(parsed, MESSAGE_METADATA_FIELD.DurationMs)
  if (completion === MessageCompletion.INTERRUPTED || reason === 'interrupted')
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (reason === 'run_error')
    return { label: turnEndLabel('failed', { durationMs, reason: commandCodeError(params?.error) }), isError: true }
  return { label: turnEndLabel('ended', { durationMs, qualifiers: [reason === 'max_turns' && 'turn limit'] }) }
}
