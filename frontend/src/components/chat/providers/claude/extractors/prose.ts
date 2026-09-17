import type { ToolCallPayload } from '../../../ir/toolCall'
import type { ReportRequest } from '../../../ir/tools/report'
import type { SkillRequest } from '../../../ir/tools/skill'
import type { WaitRequest } from '../../../ir/tools/wait'
import type { ClaudeToolRow } from './toolCommon'
import { proseResult, unparsedResult } from '../../../ir/toolCall'
import { claudeFailedResult } from './failure'

/**
 * The skill pair: which skill ran, and the words it answered with.
 * `args` is the free-text argument string the tool takes beside the name.
 *
 * The failure rung leads the ladder here, as it does in every kind whose answer is
 * prose: without it the error text drew as the skill's own answer, and an EMPTY error
 * fell to `unparsedResult`, which claims the call completed.
 */
export function claudeSkillPayload(request: SkillRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'skill'> {
  if (!result)
    return { kind: 'skill', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'skill', request, result: failure }
  if (!result.resultContent)
    return { kind: 'skill', request, result: unparsedResult(result.resultContent) }
  return { kind: 'skill', request, result: proseResult(result.resultContent) }
}

/** The wait pair: how long the call waited, and the words it answered with. */
export function claudeWaitPayload(request: WaitRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'wait'> {
  if (!result)
    return { kind: 'wait', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'wait', request, result: failure }
  if (!result.resultContent)
    return { kind: 'wait', request, result: unparsedResult(result.resultContent) }
  return { kind: 'wait', request, result: proseResult(result.resultContent) }
}

/**
 * The report pair: the turn's own structured answer. The payload is free-form
 * by design -- the schema is the model's to state -- so it rides raw.
 */
export function claudeReportPayload(request: ReportRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'report'> {
  if (!result)
    return { kind: 'report', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'report', request, result: failure }
  if (!result.resultContent)
    return { kind: 'report', request, result: unparsedResult(result.resultContent) }
  return { kind: 'report', request, result: proseResult(result.resultContent) }
}
