/**
 * Reads of an Agent Client Protocol (ACP) tool frame that every ACP provider's
 * proofs share. The Worker's `providers/acp` base serves the whole family, so
 * these reads are not one provider's wire format. A provider's own fields, such
 * as Goose's `_meta.goose` or Junie's receipt, stay in that provider's directory.
 */
import type { ACPToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { acpToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'

/** The ACP tool-call statuses that end a call. */
export const ACP_CLOSING_STATUSES: readonly string[] = ['completed', 'failed']

/**
 * Whether `frame` is the `tool_call_update` that closes call `callId` with one of
 * `statuses`. An empty call ID matches no frame.
 */
export function acpClosedToolCall(frame: Record<string, unknown>, callId: string, statuses: readonly string[] = ACP_CLOSING_STATUSES): boolean {
  return callId !== ''
    && frame.sessionUpdate === ACP_UPDATE.ToolCallUpdate
    && frame.toolCallId === callId
    && typeof frame.status === 'string'
    && statuses.includes(frame.status)
}

/**
 * The provider supplement of one ACP tool frame, through the identity gate that
 * the Worker and the browser apply. A supplement that belongs to another frame
 * throws `The native <label> record belongs to another result.`
 *
 * The gate is a precondition of the read, not the behavior that a proof shows,
 * so the proof can share the app's gate without testing the app with itself.
 */
export function requireAcpToolSupplement(original: Record<string, unknown>, supplemental: unknown, label: string): ACPToolSupplement {
  const supplement = acpToolSupplement(original, supplemental)
  if (!supplement)
    throw new Error(`The native ${label} record belongs to another result.`)
  return supplement
}
