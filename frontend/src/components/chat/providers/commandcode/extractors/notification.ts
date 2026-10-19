import type { CompactionDetails, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COMMAND_CODE_COMPACTION_OUTCOME, COMMAND_CODE_EVENT } from '~/generated/contracts/commandcode-protocol'
import { assignDefined, pickCounter, pickNumber, pickString } from '~/lib/jsonPick'
import { retryEntry, tokenCount } from '../../../model/notification'
import { COMMAND_CODE_CONTINUATION_KIND_LABEL, COMMAND_CODE_NOTICE_LEVEL, commandCodeError, commandCodeEvent } from '../protocol'

export function commandCodeNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  const event = commandCodeEvent(payload)
  if (!event)
    return []
  switch (event.type) {
    case COMMAND_CODE_EVENT.Notice: {
      const message = pickString(event, 'message')
      const prefix = COMMAND_CODE_NOTICE_LEVEL.get(pickString(event, 'level'))
      return message ? [{ kind: 'text', text: prefix ? `${prefix}: ${message}` : message }] : []
    }
    case COMMAND_CODE_EVENT.RunError:
      return [{ kind: 'text', text: commandCodeError(event.error) || 'The native model request failed.' }]
    case COMMAND_CODE_EVENT.ApiRetry:
    case COMMAND_CODE_EVENT.StreamRestart: {
      const restart = event.type === COMMAND_CODE_EVENT.StreamRestart
      const facts: RetryFacts = { error: restart ? 'The connection dropped mid-response' : commandCodeError(event.error) }
      assignDefined(facts, 'attempt', pickNumber(event, 'attempt', undefined))
      if (restart)
        assignDefined(facts, 'maxAttempts', pickNumber(event, 'maxAttempts', undefined))
      else
        assignDefined(facts, 'delayMs', pickNumber(event, 'delayMs', undefined))
      return [retryEntry('api', facts)]
    }
    case COMMAND_CODE_EVENT.CompactionOutcome:
      switch (event.outcome) {
        case COMMAND_CODE_COMPACTION_OUTCOME.Summarized:
          return [{ kind: 'compaction', phase: 'end', outcome: 'completed', detail: compactionDetails(event) }]
        case COMMAND_CODE_COMPACTION_OUTCOME.TooSmall:
          return [{ kind: 'compaction', phase: 'end', outcome: 'skipped' }]
        case COMMAND_CODE_COMPACTION_OUTCOME.Failed:
          return [{ kind: 'compaction', phase: 'end', outcome: 'failed' }]
        default:
          return []
      }
    case COMMAND_CODE_EVENT.SkillLoaded: {
      const name = pickString(event, 'name')
      return name ? [{ kind: 'text', text: `Loaded skill: ${name}` }] : []
    }
    case COMMAND_CODE_EVENT.ModError: {
      const mod = pickString(event, 'modId')
      const hook = pickString(event, 'hook')
      const error = commandCodeError(event.error)
      return [{ kind: 'text', text: `${mod ? `Mod ${mod}` : 'A mod'} failed${hook ? ` in ${hook}` : ''}${error ? `: ${error}` : ''}` }]
    }
    case COMMAND_CODE_EVENT.ContinuationRecovery: {
      const kind = pickString(event, 'kind')
      const label = COMMAND_CODE_CONTINUATION_KIND_LABEL.get(kind) ?? kind
      const attempt = pickCounter(event, 'attempt')
      const maximum = pickCounter(event, 'maxAttempts')
      const count = attempt !== undefined ? ` (${attempt}${maximum !== undefined ? `/${maximum}` : ''})` : ''
      return [{ kind: 'status', text: `The model stopped early${label ? ` (${label})` : ''}. Command Code asks it to continue${count}` }]
    }
    case COMMAND_CODE_EVENT.ToolHooks: {
      const lines = Array.isArray(event.lines) ? event.lines.filter((line): line is string => typeof line === 'string' && line.trim() !== '') : []
      return lines.length ? [{ kind: 'text', text: lines.join('\n') }] : []
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

function compactionDetails(event: Record<string, unknown>): CompactionDetails {
  const pre = tokenCount(event.tokensBefore)
  const post = tokenCount(event.tokensAfter)
  return {
    trigger: pickString(event, 'trigger') || 'auto',
    ...(pre !== undefined ? { pre } : {}),
    ...(post !== undefined ? { post } : {}),
  }
}

export function commandCodeCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const event = commandCodeEvent(parsed.parentObject)
  return event?.type === COMMAND_CODE_EVENT.CompactionOutcome && event.outcome === COMMAND_CODE_COMPACTION_OUTCOME.Summarized
    ? compactionDetails(event)
    : null
}

/** Identify a stored start for the history filter without changing its category. */
export function commandCodeCompactionStart(message: Record<string, unknown>): boolean {
  return commandCodeEvent(message)?.type === COMMAND_CODE_EVENT.CompactionStart
}
