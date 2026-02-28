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
  updatedInput?: Record<string, unknown>,
  permissionMode?: PermissionMode,
): Record<string, unknown> {
  return {
    type: 'control_response',
    permissionMode, // optional, consumed by hub for ExitPlanMode
    response: {
      subtype: 'success',
      request_id: requestId,
      response: {
        behavior: 'allow',
        updatedInput: updatedInput ?? {},
      },
    },
  }
}

/**
 * Builds a control_response JSON object that denies a tool use.
 */
export function buildDenyResponse(
  requestId: string,
  message: string,
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
        message: message || 'Rejected by user.',
      },
    },
  }
}

/**
 * Builds a control_request JSON string for changing Claude Code's permission mode.
 * The hub detects this format and sends it as raw input to Claude Code's stdin.
 * Uses the same wire protocol as the Agent SDK's setPermissionMode().
 */
export type PermissionMode = 'default' | 'acceptEdits' | 'plan' | 'bypassPermissions'

/** Default model and effort level for new agents. */
export const DEFAULT_MODEL = import.meta.env.LEAPMUX_DEFAULT_MODEL || 'opus'
export const DEFAULT_EFFORT = import.meta.env.LEAPMUX_DEFAULT_EFFORT || 'high'

/** Display labels for permission modes, models, and effort levels. */
export const PERMISSION_MODE_LABELS: Record<PermissionMode, string> = {
  default: 'Default',
  plan: 'Plan Mode',
  acceptEdits: 'Accept Edits',
  bypassPermissions: 'Bypass Permissions',
}

export const MODEL_LABELS: Record<string, string> = {
  opus: 'Opus',
  sonnet: 'Sonnet',
  haiku: 'Haiku',
}

export const EFFORT_LABELS: Record<string, string> = {
  high: 'High',
  medium: 'Medium',
  low: 'Low',
}

export function buildSetPermissionModeRequest(mode: PermissionMode): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
    request: {
      subtype: 'set_permission_mode',
      mode,
    },
  })
}

/**
 * Builds a control_request JSON string for interrupting a running agent turn.
 */
export function buildInterruptRequest(): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
    request: {
      subtype: 'interrupt',
    },
  })
}

function generateRandomId(): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  let result = '01'
  for (let i = 0; i < 22; i++) {
    result += chars[Math.floor(Math.random() * chars.length)]
  }
  return result
}
