import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { canonicalClaudeToolName } from '../toolKinds'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { getMessageContentArray } from './assistantContent'

/**
 * The plan an `ExitPlanMode` call proposes, or '' for every other call.
 *
 * ONE reader, because BOTH layers of the pipeline ask the question and they must
 * agree: `classify` answers `assistant_plan` for a frame this finds a plan in, and
 * `claudeExtractRow` draws that plan. While the classifier said `tool_use` and the
 * extractor said `assistant-plan`, the virtual list measured a tool row and the
 * transcript painted a plan card into it.
 *
 * A call with NO plan keeps the tool path: the reader gets the ordinary
 * `ExitPlanMode` row, which states the approval it received.
 */
export function claudeExitPlanText(toolUse: Record<string, unknown> | undefined): string {
  if (!toolUse || canonicalClaudeToolName(pickString(toolUse, 'name')) !== CLAUDE_TOOL_NAMES.EXIT_PLAN_MODE)
    return ''
  return pickString(pickObject(toolUse, 'input'), 'plan').trim()
}

/** The plan a whole Claude assistant envelope proposes, or '' when it proposes none. */
export function claudePlanFromEnvelope(payload: Record<string, unknown> | undefined): string {
  const block = getMessageContentArray(payload)?.find(item => isObject(item) && item.type === 'tool_use')
  return claudeExitPlanText(isObject(block) ? block : undefined)
}
