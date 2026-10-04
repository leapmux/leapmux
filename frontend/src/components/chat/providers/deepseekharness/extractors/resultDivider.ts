import type { TurnEnd } from '../../../model/divider'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { deepseekHarnessEventData } from '../protocol'

export function deepseekHarnessResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  const data = deepseekHarnessEventData(parsed, DEEPSEEK_HARNESS_EVENT.TurnEnd)
  if (!data)
    return null
  const reason = pickObject(data, 'reason')
  const kind = pickString(reason, 'kind')
  if (completion === MessageCompletion.INTERRUPTED || kind === 'interrupted' || kind === 'aborted')
    return { label: turnEndLabel('interrupted') }
  if (kind !== 'completed' && kind !== 'max-tokens')
    return { label: turnEndLabel('failed', { reason: pickString(pickObject(reason, 'error'), 'message') || kind || 'Native turn failed' }), isError: true }
  return { label: turnEndLabel('ended', { qualifiers: [kind === 'max-tokens' && 'token limit'] }) }
}
