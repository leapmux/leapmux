import { KIRO_META } from '~/generated/contracts/kiro-protocol'
import { pickObject } from '~/lib/jsonPick'

/**
 * Kiro's wire words that only the browser reads, so they are not in the contract.
 */

/** The `_meta.kiro.type` of the request that reviews the file changes of a Supervised turn. */
export const KIRO_TURN_APPROVAL = 'turn_approval'

/**
 * The key of Kiro's reply that carries the reason of a rejected permission. Kiro gives
 * the text to the model beside the rejection.
 */
export const KIRO_REJECTION_REASON = 'rejectionReason'

/**
 * The `_meta.kiro` object of one frame: a request's params, a tool call, an update or
 * a reply. Kiro states each of its own fields there. Undefined when the frame carries
 * none.
 */
export function kiroMeta(frame: Record<string, unknown> | null | undefined): Record<string, unknown> | undefined {
  return pickObject(pickObject(frame, '_meta'), KIRO_META.Namespace) ?? undefined
}
