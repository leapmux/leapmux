import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { antigravityRowOpen, antigravityStepHeader } from './extractors/toolCommon'

const NON_TOOL_STEPS = new Set<string>([
  ANTIGRAVITY_STEP_TYPE.Checkpoint,
  ANTIGRAVITY_STEP_TYPE.ErrorMessage,
  ANTIGRAVITY_STEP_TYPE.PlannerResponse,
  ANTIGRAVITY_STEP_TYPE.SystemMessage,
  ANTIGRAVITY_STEP_TYPE.UserInput,
])

/** An open step is the request row of its call, and a finished step or a closing copy is the result row. */
export function antigravitySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const step = antigravityStepHeader(parsed.parentObject)
  if (!step || parsed.wrapper || NON_TOOL_STEPS.has(step.type))
    return 'other'
  return antigravityRowOpen(step, parsed.completion)
    ? 'request'
    : 'result'
}
