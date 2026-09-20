import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ReportRequest } from '../../../model/tools/report'
import type { SkillRequest } from '../../../model/tools/skill'
import type { WaitRequest } from '../../../model/tools/wait'
import type { ClaudeToolRow } from './toolCommon'
import { proseResult, unparsedResult } from '../../../model/toolCall'
import { claudeToolFailureResult } from './failure'

/**
 * The skill pair: which skill ran, and the words it answered with.
 * `args` is the free-text argument string the tool takes beside the name.
 *
 * The failure rung leads the ladder here, as it does in every kind whose answer is
 * prose: without it the error text drew as the skill's own answer, and an EMPTY error
 * fell to `unparsedResult`, which claims the call completed.
 */
export function claudeSkillSpec(request: SkillRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'skill'> {
  if (!result)
    return { kind: 'skill', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'skill', request, result: failure }
  if (!result.resultContent)
    return { kind: 'skill', request, result: unparsedResult(result.resultContent) }
  return { kind: 'skill', request, result: proseResult(result.resultContent) }
}

/** The wait pair: how long the call waited, and the words it answered with. */
export function claudeWaitSpec(request: WaitRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'wait'> {
  if (!result)
    return { kind: 'wait', request }
  const failure = claudeToolFailureResult(result)
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
export function claudeReportSpec(request: ReportRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'report'> {
  if (!result)
    return { kind: 'report', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'report', request, result: failure }
  if (!result.resultContent)
    return { kind: 'report', request, result: unparsedResult(result.resultContent) }
  return { kind: 'report', request, result: proseResult(result.resultContent) }
}
