import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { pickString } from '~/lib/jsonPick'
import { deepseekHarnessEventData } from '../protocol'

export function deepseekHarnessNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  const data = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.CompactionEnd)
  if (!data)
    return []
  const error = pickString(data, 'error')
  return error
    ? [{ kind: 'text', text: `Native compaction failed: ${error}` }]
    : [{ kind: 'compaction', phase: 'end', detail: {} }]
}

export function deepseekHarnessCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const data = deepseekHarnessEventData(parsed.parentObject, DEEPSEEK_HARNESS_EVENT.CompactionEnd)
  return data && data.error === undefined ? {} : null
}
