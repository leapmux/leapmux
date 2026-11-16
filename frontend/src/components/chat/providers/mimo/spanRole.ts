import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type {} from '../registry'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { mimoToolPart, mimoToolSpanRole } from './extractors/toolCommon'

/**
 * MiMo span role. A tool part's STATUS discriminates the opening row from the final
 * one, because both halves are the same event type.
 *
 * MiMo has no `needsRequest` hook: each frame states the whole call state, the input
 * included, so no row reads its span's request.
 */
export function mimoSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const part = mimoToolPart(parsed.parentObject)
  return part
    ? mimoToolSpanRole(part, parsed.completion)
    : 'other'
}
