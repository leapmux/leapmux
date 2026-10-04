import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type {} from '../registry'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { mimoToolPart, mimoToolSpanRole } from './extractors/toolCommon'

/**
 * MiMo span role. A tool part's STATUS discriminates the opening row from the final
 * one, because both halves are the same event type.
 */
export function mimoSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const part = mimoToolPart(parsed.parentObject)
  return part ? mimoToolSpanRole(part, parsed.completion) : 'other'
}

/** The request pairs with its result, which states the call's final status. */
export function mimoRelatedMessages(parsed: ResolvedMessageContent) {
  const role = mimoSpanRole(parsed)
  if (role === 'result')
    return ['request'] as const
  if (role === 'request')
    return ['result'] as const
  return []
}
