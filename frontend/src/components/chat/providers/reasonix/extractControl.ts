import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { REASONIX_TOOL } from '~/generated/contracts/reasonix-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { acpExtractControl, acpPermissionToolCall } from '../acp/extractControl'

/** Whether one request is Reasonix's native plan gate. */
export function isReasonixPlanApproval(payload: Record<string, unknown>): boolean {
  if (pickString(payload, 'method') !== 'session/request_permission')
    return false
  const metadata = pickObject(pickObject(acpPermissionToolCall(payload), '_meta'), 'reasonix.io')
  return pickString(metadata, 'tool') === REASONIX_TOOL.ExitPlanMode
}

/**
 * Reasonix streams the proposal before its exit request, so the shared plan
 * surface needs no duplicate text. Other requests keep the ACP permission reader.
 */
export function reasonixExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  if (isReasonixPlanApproval(input.payload))
    return { kind: 'plan' }
  return acpExtractControl(input)
}
