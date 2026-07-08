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
 * Builds a control_response JSON object that allows a tool use.
 */
export function buildAllowResponse(
  requestId: string,
  updatedInput: Record<string, unknown>,
  opts?: { permissionMode?: PermissionMode, clearContext?: boolean },
): Record<string, unknown> {
  return {
    type: 'control_response',
    permissionMode: opts?.permissionMode,
    ...(opts?.clearContext ? { clearContext: true } : {}),
    response: {
      subtype: 'success',
      request_id: requestId,
      response: {
        behavior: 'allow',
        updatedInput,
      },
    },
  }
}

/**
 * The placeholder reject message emitted when the user declines a control request WITHOUT
 * typing a reason. Must stay byte-identical to the backend's `ControlRejectedByUserMessage`
 * (backend/internal/worker/agent/factory.go), whose `NormalizeRejectionMessage` collapses it
 * to "" so a bare deny renders "Rejected" rather than leaking this placeholder as if it were
 * typed feedback. Kept as ONE frontend constant so the producer sites (buildDenyResponse's
 * default and any caller wanting a bare deny) can't drift from each other.
 */
export const CONTROL_REJECTED_BY_USER_MESSAGE = 'Rejected by user.'

/**
 * Builds a control_response JSON object that denies a tool use. Omit `message` for a bare
 * deny with no typed reason (it falls back to CONTROL_REJECTED_BY_USER_MESSAGE).
 */
export function buildDenyResponse(
  requestId: string,
  message?: string,
): Record<string, unknown> {
  return {
    type: 'control_response',
    response: {
      subtype: 'success',
      request_id: requestId,
      response: {
        behavior: 'deny',
        // Ensure the message is never empty — Claude Code SDK converts deny
        // responses into tool_result with is_error=true, and the Anthropic API
        // rejects empty content when is_error is set.
        message: message || CONTROL_REJECTED_BY_USER_MESSAGE,
      },
    },
  }
}

/**
 * Trim a control-response reject reason and collapse the
 * {@link CONTROL_REJECTED_BY_USER_MESSAGE} placeholder (the auto-filled "declined without a
 * reason" text) to "". Mirrors the backend's `NormalizeRejectionMessage` so a bare deny reads
 * as "Rejected" on both sides instead of leaking the placeholder as typed feedback.
 */
export function normalizeRejectionMessage(message: string): string {
  const trimmed = message.trim()
  return trimmed === CONTROL_REJECTED_BY_USER_MESSAGE ? '' : trimmed
}

/**
 * Decode the neutral approve/reject control-response envelope
 * (`{response:{request_id, response:{behavior, message}}}`) from an already-parsed OBJECT, the
 * shape `buildAllowResponse` / `buildDenyResponse` produce and the Claude/Codex-envelope native
 * response persists. Returns the trimmed request id, behavior, and rejection message (with the
 * {@link CONTROL_REJECTED_BY_USER_MESSAGE} sentinel collapsed to ""), or null when the object
 * doesn't carry a recognizable allow/deny behavior. The single home for reading this shape,
 * shared by the byte-oriented {@link decodeControlResponseBehavior} and the persisted-row
 * renderer (persistedControlResponse.ts).
 */
export function decodeControlBehaviorEnvelope(
  obj: unknown,
): { requestId: string, behavior: 'allow' | 'deny', message: string } | null {
  if (!isObject(obj))
    return null
  const outer = isObject(obj.response) ? obj.response : undefined
  const inner = isObject(outer?.response) ? outer.response as Record<string, unknown> : undefined
  const behavior = pickString(inner, 'behavior', '').trim()
  if (behavior !== 'allow' && behavior !== 'deny')
    return null
  return {
    requestId: pickString(outer, 'request_id', '').trim(),
    behavior,
    message: normalizeRejectionMessage(pickString(inner, 'message', '')),
  }
}

/**
 * Decode the inverse of `buildAllowResponse` / `buildDenyResponse`: a
 * `control_response` envelope's serialized bytes back into its `behavior`.
 * Returns null on any decode failure or when the envelope doesn't carry a
 * recognizable behavior — callers can then fall back to their own state.
 *
 * Providers that want to translate the shared AskUserQuestion
 * response into their own wire format (e.g. Pi's `extension_ui_response`
 * shape) call this helper instead of reaching into the nested
 * `response.response` themselves.
 */
export function decodeControlResponseBehavior(content: Uint8Array): 'allow' | 'deny' | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(new TextDecoder().decode(content))
  }
  catch {
    return null
  }
  return decodeControlBehaviorEnvelope(parsed)?.behavior ?? null
}

/**
 * Builds a control_request JSON string for changing Claude Code's permission mode.
 * The hub detects this format and sends it as raw input to Claude Code's stdin.
 * Uses the same wire protocol as the Agent SDK's setPermissionMode().
 */
export type PermissionMode = string
