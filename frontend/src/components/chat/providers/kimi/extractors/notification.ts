import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_CONVERSATION_ORIGIN, KIMI_CONVERSATION_OUTCOME, KIMI_CONVERSATION_RECORD_ROLE, KIMI_CONVERSATION_STATUS, KIMI_CONVERSATION_SYSTEM_KIND, KIMI_EVENT, KIMI_NOTICE_LEVEL } from '~/generated/contracts/kimi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { kimiConversationRow, kimiTurnIsBlocked } from '../conversation'
import { kimiEvent } from '../protocol'

/**
 * Read one Kimi Code notification row into the shared notification model.
 *
 * The SOLE notification seam, for a standalone row and for one entry of a consolidated
 * thread alike. Returns an empty list for a row this provider does not own, and for a
 * row of its own that states nothing it can word.
 */
export function kimiNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const conversation = kimiConversationRow(msg)
  if (conversation?.kind === 'item' && conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Origin) {
    const explanation = kimiConversationOriginExplanation(pickObject(conversation.item, KIMI_CONVERSATION_FIELD.Origin))
    return explanation
      ? [
          { kind: 'text', text: explanation },
        ]
      : []
  }
  if (conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.Notification) {
    const item = conversation.item
    const message = [
      pickString(item, KIMI_CONVERSATION_FIELD.Title),
      pickString(item, KIMI_CONVERSATION_FIELD.Body),
    ]
      .filter(text => text.trim())
      .join(': ')
    return kimiNoticeEntries(pickString(item, KIMI_CONVERSATION_FIELD.Severity), message, '')
  }
  if (conversation?.kind === 'failure') {
    const detail = pickString(conversation.failure, KIMI_CONVERSATION_FIELD.Msg).trim()
    return [
      {
        kind: 'text',
        text: `Conversation history could not be loaded${detail
          ? `: ${detail}`
          : ''} (code ${Number(conversation.failure[KIMI_CONVERSATION_FIELD.Code])})`,
      },
    ]
  }
  if (conversation?.kind === 'retry') {
    const error = pickObject(conversation.retry, KIMI_CONVERSATION_FIELD.Error)
    return [
      retryEntry('api', {
        attempt: Number(conversation.retry[KIMI_CONVERSATION_FIELD.NextAttempt]),
        maxAttempts: Number(conversation.retry[KIMI_CONVERSATION_FIELD.MaxAttempts]),
        delayMs: Number(conversation.retry[KIMI_CONVERSATION_FIELD.DelayMS]),
        error: pickString(error, 'message').trim() || pickString(error, 'name').trim(),
      }),
    ]
  }
  if (conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.System
    && conversation.item[KIMI_CONVERSATION_FIELD.Kind] === KIMI_CONVERSATION_SYSTEM_KIND.Compaction && conversation.item[KIMI_CONVERSATION_FIELD.Status] === KIMI_CONVERSATION_STATUS.Ended) {
    switch (conversation.item[KIMI_CONVERSATION_FIELD.Outcome]) {
      case KIMI_CONVERSATION_OUTCOME.Completed:
        return [
          {
            kind: 'compaction',
            phase: 'end',
            detail: kimiCurrentCompactionDetails(conversation.item),
          },
        ]
      case KIMI_CONVERSATION_OUTCOME.Interrupted:
        return [
          { kind: 'compaction', phase: 'end', outcome: 'aborted' },
        ]
      case KIMI_CONVERSATION_OUTCOME.Failed:
        return [
          { kind: 'compaction', phase: 'end', outcome: 'failed' },
        ]
    }
  }
  if (conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.AssistantMessage && kimiTurnIsBlocked(conversation.turnContext)) {
    const hook = /^<hook_result hook_event="([^"<>\n]+)">\n([\s\S]*)\n<\/hook_result>$/.exec(pickString(conversation.item, KIMI_CONVERSATION_FIELD.Text))
    if (hook) {
      const detail = hook[2]?.trim()
      return [
        { kind: 'text', text: `Hook ${hook[1]} blocked${detail
          ? `: ${detail}`
          : ''}` },
      ]
    }
  }
  if (conversation?.kind === 'notice') {
    const notice = conversation.notice
    const message = [
      pickString(notice, KIMI_CONVERSATION_FIELD.Title),
      pickString(notice, KIMI_CONVERSATION_FIELD.Detail),
    ]
      .filter(text => text.trim())
      .join(': ')
    return kimiNoticeEntries(pickString(notice, KIMI_CONVERSATION_FIELD.Level), message, pickString(notice, KIMI_CONVERSATION_FIELD.Code))
  }
  const event = kimiEvent(msg)
  if (!event)
    return []
  const data = event.data
  switch (event.type) {
    case KIMI_EVENT.ConfigWarning:
      return (Array.isArray(data.warnings)
        ? data.warnings
        : []).flatMap((warning): NotificationEntry[] => {
        const message = isObject(warning)
          ? pickString(warning, 'message')
          : ''
        return message.trim()
          ? [
              { kind: 'text', text: `Warning: ${message}` },
            ]
          : []
      })
    case KIMI_EVENT.Warning: {
      const message = pickString(data, 'message')
      return message
        ? [
            { kind: 'text', text: `Warning: ${message}` },
          ]
        : []
    }
    default:
      return []
  }
}

function kimiConversationOriginExplanation(origin: Record<string, unknown> | null): string {
  const field = KIMI_CONVERSATION_FIELD
  switch (origin?.[field.Kind]) {
    case KIMI_CONVERSATION_ORIGIN.Goal:
      return 'Continuing the goal'
    case KIMI_CONVERSATION_ORIGIN.Retry:
      return 'Retrying the turn'
    case KIMI_CONVERSATION_ORIGIN.Cron: {
      const jobId = pickString(origin, field.JobID)
      if (origin[field.Stale] === true) {
        return jobId
          ? `Missed scheduled prompt ${jobId} ran late`
          : 'A missed scheduled prompt ran late'
      }
      return jobId
        ? `Scheduled prompt ${jobId} started a turn`
        : 'A scheduled prompt started a turn'
    }
    case KIMI_CONVERSATION_ORIGIN.Hook: {
      const event = pickString(origin, field.HookEvent)
      return event
        ? `Hook ${event} started a turn`
        : 'A hook started a turn'
    }
    case KIMI_CONVERSATION_ORIGIN.Skill: {
      const name = pickString(origin, field.Name)
      return name
        ? `Skill ${name} started a turn`
        : 'A skill started a turn'
    }
    case KIMI_CONVERSATION_ORIGIN.PluginCommand: {
      const plugin = pickString(origin, field.PluginID)
      const command = pickString(origin, field.Command)
      return plugin && command
        ? `Plugin command ${plugin}/${command} started a turn`
        : 'A plugin command started a turn'
    }
    default:
      return ''
  }
}

function kimiNoticeEntries(level: string, message: string, code: string): NotificationEntry[] {
  if (!message.trim()) {
    return level === KIMI_NOTICE_LEVEL.Error && code.trim()
      ? [
          { kind: 'text', text: `Error: ${code}` },
        ]
      : []
  }
  const prefix = level === KIMI_NOTICE_LEVEL.Error
    ? code
      ? `Error (${code}): `
      : 'Error: '
    : level === KIMI_NOTICE_LEVEL.Warning
      ? 'Warning: '
      : ''
  return [
    { kind: 'text', text: `${prefix}${message}` },
  ]
}

function kimiCurrentCompactionDetails(item: Record<string, unknown>): CompactionDetails {
  const pre = pickNumber(item, KIMI_CONVERSATION_FIELD.TokensBefore)
  const post = pickNumber(item, KIMI_CONVERSATION_FIELD.TokensAfter)
  const trigger = pickString(item, KIMI_CONVERSATION_FIELD.Trigger)
  return {
    ...(trigger
      ? { trigger }
      : {}),
    ...(pre !== null
      ? { pre }
      : {}),
    ...(post !== null
      ? { post }
      : {}),
  }
}

/**
 * The compaction boundary one row states, or null for any other row.
 *
 * The context-usage grid reads it the moment the boundary lands, so the gauge drops to
 * the compacted size before the next usage report arrives.
 */
export function kimiCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const conversation = kimiConversationRow(parsed.parentObject)
  if (conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.System
    && conversation.item[KIMI_CONVERSATION_FIELD.Kind] === KIMI_CONVERSATION_SYSTEM_KIND.Compaction && conversation.item[KIMI_CONVERSATION_FIELD.Status] === KIMI_CONVERSATION_STATUS.Ended
    && conversation.item[KIMI_CONVERSATION_FIELD.Outcome] === KIMI_CONVERSATION_OUTCOME.Completed) {
    return kimiCurrentCompactionDetails(conversation.item)
  }
  return null
}
