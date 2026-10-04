import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { QWEN_META, QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { acpExtractControl, acpPermissionToolCall } from '../acp/extractControl'

/**
 * Whether one control request is Qwen's plan approval.
 *
 * Qwen raises it as a standard permission request about its `exit_plan_mode` tool
 * call. The worker reads the same mark to write the reply the reader's decision
 * stands for.
 */
export function isQwenPlanApproval(payload: Record<string, unknown>): boolean {
  return pickString(pickObject(acpPermissionToolCall(payload), '_meta'), QWEN_META.ToolName) === QWEN_TOOL.ExitPlanMode
}

/**
 * `Provider.extractControl` for Qwen Code.
 *
 * The plan approval takes the shared plan surface, with the plan the tool call
 * states. Its four options are Qwen's own ways to leave plan mode, and the worker
 * picks one from the reader's decision and the mode the reader chose, so the reader
 * answers with Approve or Reject. Every other request is a standard Agent Client
 * Protocol permission, which the shared reader draws.
 */
export function qwenExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  if (isQwenPlanApproval(input.payload)) {
    const text = pickString(pickObject(acpPermissionToolCall(input.payload), 'rawInput'), 'plan')
    return { kind: 'plan', ...(text ? { text } : {}) }
  }
  return acpExtractControl(input)
}
