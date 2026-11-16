import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { antigravityRowOpen, antigravityStepHeader } from './extractors/toolCommon'

/** An open step is the request row of its call, and a finished step or a closing copy is the result row. */
export function antigravitySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const step = antigravityStepHeader(parsed.parentObject)
  if (!step)
    return 'other'
  return antigravityRowOpen(step, parsed.completion)
    ? 'request'
    : 'result'
}
