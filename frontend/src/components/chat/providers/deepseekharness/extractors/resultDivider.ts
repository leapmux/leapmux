import type { TurnEnd } from '../../../model/divider'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { deepseekHarnessEventData } from '../protocol'

/**
 * A DeepSeek Harness `turn/end` session event read into the turn-end divider, or null for another row.
 *
 * The native event states no duration. The Worker measures each turn from the times of its native start and end
 * events, and adds the duration to the turn end as its own metadata, which the reader merges into the event.
 */
export function deepseekHarnessResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  const data = deepseekHarnessEventData(parsed, DEEPSEEK_HARNESS_EVENT.TurnEnd)
  if (!data)
    return null
  const reason = pickObject(data, 'reason')
  const kind = pickString(reason, 'kind')
  const durationMs = pickNumber(isObject(parsed) ? parsed : undefined, MESSAGE_METADATA_FIELD.DurationMs)
  if (completion === MessageCompletion.INTERRUPTED || kind === 'interrupted' || kind === 'aborted')
    return { label: turnEndLabel('interrupted', { durationMs }) }
  if (kind !== 'completed' && kind !== 'max-tokens')
    return { label: turnEndLabel('failed', { durationMs, reason: pickString(pickObject(reason, 'error'), 'message') || kind || 'Native turn failed' }), isError: true }
  return { label: turnEndLabel('ended', { durationMs, qualifiers: [kind === 'max-tokens' && 'token limit'] }) }
}
