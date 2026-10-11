import type { ToolCallSpec } from '../model/toolCall'
import { failedResult } from '../model/toolCall'

/**
 * The specification of a call that the reader or a rule REFUSED, so the tool never ran.
 *
 * The model admits one shape for that state (`DeclinedToolCallState`): the refusal as a
 * failure result, and no pictures, no extra content and no truncation mark. A provider's
 * builder reads the frame as if the call answered, and several builders attach a body or
 * an artifact whatever the status. This helper replaces the result and drops each artifact
 * in one place, so no builder must remember the rule.
 *
 * A provider that recognizes a refusal in its own frame applies this helper around its
 * whole build. Only the provider knows which frame states a refusal, so the recognition
 * stays in the provider, and this helper reads no wire format.
 *
 * `reason` is the provider's own words for the refusal. The row draws them as its body.
 */
export function declinedToolCallSpec(spec: ToolCallSpec, reason: string): ToolCallSpec {
  const { extraContent, truncated, ...payload } = spec
  return { ...payload, images: [], statusOverride: 'declined', result: failedResult(reason) }
}
