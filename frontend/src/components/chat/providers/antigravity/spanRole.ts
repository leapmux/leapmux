import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { antigravityStep, antigravityStepOpen } from './extractors/toolCommon'

export function antigravitySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const step = antigravityStep(parsed.parentObject)
  if (!step)
    return 'other'
  return antigravityStepOpen(step)
    ? 'request'
    : 'result'
}

export function antigravityRelatedMessages(parsed: ResolvedMessageContent) {
  const role = antigravitySpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  if (role === 'request')
    return ['result'] as const
  return []
}
