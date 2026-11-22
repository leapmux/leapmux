import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { antigravityRowOpen, antigravityStepDisposition, antigravityStepHeader } from './extractors/toolCommon'

/** An open step is the request row of its call, and a finished step or a closing copy is the result row. */
export function antigravitySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const step = antigravityStepHeader(parsed.parentObject)
  if (!step || parsed.wrapper || antigravityStepDisposition(step.type) !== 'tool')
    return 'other'
  return antigravityRowOpen(step, parsed.completion)
    ? 'request'
    : 'result'
}
