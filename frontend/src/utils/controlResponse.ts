import type { ControlAnswerBehavior } from '~/generated/contracts/control-answer'
import { CONTROL_ANSWER_BEHAVIOR, CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { isObject, pickString } from '~/lib/jsonPick'

/** Extract the tool_name from a control_request payload */
export function getToolName(payload: Record<string, unknown>): string {
  const request = payload.request as Record<string, unknown> | undefined
  return (request?.tool_name as string) ?? ''
}

/** Extract the input from a control_request payload */
export function getToolInput(payload: Record<string, unknown>): Record<string, unknown> {
  const request = payload.request as Record<string, unknown> | undefined
  return (request?.input as Record<string, unknown>) ?? {}
}

/**
 * Trim a control-response reject reason and collapse the
 * `CONTROL_ANSWER_MESSAGE.RejectedByUser` placeholder (the auto-filled "declined without a
 * reason" text) to "". Mirrors the backend's `NormalizeRejectionMessage` so a bare deny reads
 * as "Rejected" on both sides instead of leaking the placeholder as typed feedback.
 */
export function normalizeRejectionMessage(message: string): string {
  const trimmed = message.trim()
  return trimmed === CONTROL_ANSWER_MESSAGE.RejectedByUser
    ? ''
    : trimmed
}

/**
 * Decode the neutral approve/reject control-response envelope
 * (`{response:{request_id, response:{behavior, message}}}`) from an already-parsed OBJECT, the
 * shape that the Worker's neutral answer takes and that a persisted row stores. Returns the
 * trimmed request id, behavior, and rejection message (with the
 * `CONTROL_ANSWER_MESSAGE.RejectedByUser` sentinel collapsed to ""), or null when the object
 * doesn't carry a recognizable allow/deny behavior. The single home for reading this shape,
 * shared by the persisted-row renderers (persistedControlResponse.ts).
 */
export function decodeControlBehaviorEnvelope(
  obj: unknown,
): { requestId: string, behavior: ControlAnswerBehavior, message: string } | null {
  if (!isObject(obj))
    return null
  const outer = isObject(obj.response)
    ? obj.response
    : undefined
  const inner = isObject(outer?.response)
    ? outer.response as Record<string, unknown>
    : undefined
  const behavior = pickString(inner, 'behavior', '').trim()
  if (behavior !== CONTROL_ANSWER_BEHAVIOR.Allow && behavior !== CONTROL_ANSWER_BEHAVIOR.Deny)
    return null
  return {
    requestId: pickString(outer, 'request_id', '').trim(),
    behavior,
    message: normalizeRejectionMessage(pickString(inner, 'message', '')),
  }
}

/** A permission mode, as the provider spells it, such as Claude Code's `acceptEdits`. */
export type PermissionMode = string
