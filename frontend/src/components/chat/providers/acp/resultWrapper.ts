import { ACP_ROLE } from '~/generated/contracts/acp-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'

/**
 * Resolve an ACP `session/prompt` answer to the object that holds the turn
 * fields (`stopReason`, `usage`).
 *
 * Some ACP servers put those fields at the top level:
 *
 *     {stopReason, usage, ...}
 *
 * Others wrap them in a native result envelope:
 *
 *     {id, role: "result", seq, created_at, content: {stopReason, usage, ...}}
 *
 * The worker persists the answer byte for byte, so the browser resolves the
 * wrapper. The `role` token comes from `contracts/acp-protocol.json`, which the
 * Go worker reads also: `handleACPUpdate` drops a `session/update` that carries
 * it. This function is the ONE site that knows the wrapper. The
 * classifier, the turn-end divider and the tool presentation all call it, so
 * the three cannot disagree about which object holds `stopReason`. A
 * classifier that read the wrapper while the divider read the content gave a
 * raw JSON bubble where the turn-end divider belongs.
 *
 * Returns the envelope unchanged for an unwrapped answer, and for a wrapper
 * whose `content` is not an object. Returns undefined for a non-object input,
 * so each caller keeps its own "not my shape" path.
 */
export function unwrapACPResult(envelope: unknown): Record<string, unknown> | undefined {
  if (!isObject(envelope))
    return undefined
  if (envelope.role !== ACP_ROLE.Result)
    return envelope
  return pickObject(envelope, 'content') ?? envelope
}
