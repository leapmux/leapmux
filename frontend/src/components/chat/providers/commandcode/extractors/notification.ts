import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COMMAND_CODE_EVENT } from '~/generated/contracts/commandcode-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { commandCodeError, commandCodeEvent } from '../protocol'

export function commandCodeNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  const event = commandCodeEvent(payload)
  if (!event)
    return []
  switch (event.type) {
    case COMMAND_CODE_EVENT.Notice:
      return pickString(event, 'message') ? [{ kind: 'text', text: pickString(event, 'message') }] : []
    case COMMAND_CODE_EVENT.RunError:
      return [{ kind: 'text', text: commandCodeError(event.error) || 'The native model request failed.' }]
    case COMMAND_CODE_EVENT.ApiRetry: {
      const attempt = pickNumber(event, 'attempt', undefined)
      const delayMs = pickNumber(event, 'delayMs', undefined)
      return [{
        kind: 'retry',
        scope: 'api',
        ...(attempt !== undefined ? { attempt } : {}),
        ...(delayMs !== undefined ? { delayMs } : {}),
        error: commandCodeError(event.error),
      }]
    }
    case COMMAND_CODE_EVENT.CompactionStart:
      return [{ kind: 'compaction', phase: 'start', detail: { trigger: pickString(event, 'trigger') } }]
    case COMMAND_CODE_EVENT.CompactionDone:
      return event.trigger !== 'manual' && (pickNumber(event, 'tokensSaved') ?? 0) > 0 ? [{ kind: 'compaction', phase: 'end', detail: {} }] : []
    case COMMAND_CODE_EVENT.CompactionOutcome: {
      if (event.outcome === 'failed')
        return [{ kind: 'text', text: 'Native compaction failed.' }]
      if (event.trigger === 'manual' && event.outcome === 'summarized')
        return [{ kind: 'compaction', phase: 'end', detail: manualCompactionDetails(event) }]
      return []
    }
    case COMMAND_CODE_EVENT.SubagentProgress: {
      const tool = pickString(event, 'toolName')
      const input = pickString(event, 'toolInput')
      return tool ? [{ kind: 'status', text: input ? `${tool}: ${input}` : tool }] : []
    }
    default:
      return []
  }
}

function manualCompactionDetails(event: Record<string, unknown>): CompactionDetails {
  const pre = pickNumber(event, 'tokensBefore', undefined)
  const post = pickNumber(event, 'tokensAfter', undefined)
  return {
    trigger: 'manual',
    ...(pre !== undefined && pre >= 0 && Number.isFinite(pre) ? { pre } : {}),
    ...(post !== undefined && post >= 0 && Number.isFinite(post) ? { post } : {}),
  }
}

export function commandCodeCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const event = commandCodeEvent(parsed.parentObject)
  return event?.type === COMMAND_CODE_EVENT.CompactionOutcome && event.trigger === 'manual' && event.outcome === 'summarized'
    ? manualCompactionDetails(event)
    : null
}
