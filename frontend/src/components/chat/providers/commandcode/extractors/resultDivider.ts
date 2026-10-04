import type { TurnEnd } from '../../../model/divider'
import { COMMAND_CODE_METHOD } from '~/generated/contracts/commandcode-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { commandCodeError } from '../protocol'

export function commandCodeResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed) || parsed.method !== COMMAND_CODE_METHOD.TurnCompleted)
    return null
  const params = pickObject(parsed, 'params')
  const reason = pickString(params, 'stopReason')
  if (completion === MessageCompletion.INTERRUPTED || reason === 'interrupted')
    return { label: turnEndLabel('interrupted') }
  if (reason === 'run_error')
    return { label: turnEndLabel('failed', { reason: commandCodeError(params?.error) }), isError: true }
  return { label: turnEndLabel('ended', { qualifiers: [reason === 'max_turns' && 'turn limit'] }) }
}
