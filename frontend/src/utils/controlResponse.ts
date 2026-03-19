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

/** Default model and effort level for new Claude Code agents. */
export const DEFAULT_MODEL = import.meta.env.LEAPMUX_DEFAULT_CLAUDE_MODEL || 'opus'
export const DEFAULT_EFFORT = import.meta.env.LEAPMUX_DEFAULT_EFFORT || 'high'

/** Default model for Codex agents. */
export const DEFAULT_CODEX_MODEL = import.meta.env.LEAPMUX_DEFAULT_CODEX_MODEL || 'gpt-5.4'
export const DEFAULT_CODEX_EFFORT = 'medium'

/** Display labels for permission modes, models, and effort levels. */
export const PERMISSION_MODE_LABELS: Record<PermissionMode, string> = {
  default: 'Default',
  plan: 'Plan Mode',
  acceptEdits: 'Accept Edits',
  bypassPermissions: 'Bypass Permissions',
}

export const MODEL_LABELS: Record<string, string> = {
  'opus': 'Opus',
  'opus[1m]': 'Opus[1m]',
  'sonnet': 'Sonnet',
  'sonnet[1m]': 'Sonnet[1m]',
  'haiku': 'Haiku',
}

export const CODEX_MODEL_LABELS: Record<string, string> = {
  'o4-mini': 'o4-mini',
  'o3': 'o3',
  'gpt-5.4': 'GPT-5.4',
  'codex-mini': 'Codex Mini',
}

export const EFFORT_LABELS: Record<string, string> = {
  auto: 'Auto',
  max: 'Max',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
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

let codexReqIdCounter = 1000

/**
 * Builds a JSON-RPC request for interrupting a Codex turn.
 */
export function buildCodexInterruptRequest(threadId: string, turnId: string): string {
  return JSON.stringify({
    jsonrpc: '2.0',
    id: ++codexReqIdCounter,
    method: 'turn/interrupt',
    params: { threadId, turnId },
  })
}

/**
 * Builds a JSON-RPC response for a Codex approval request (allow).
 */
export function buildCodexApprovalResponse(requestId: number, approved: boolean, decision?: string): string {
  if (approved) {
    return JSON.stringify({
      jsonrpc: '2.0',
      id: requestId,
      result: { decision: decision || 'allow' },
    })
  }
  return JSON.stringify({
    jsonrpc: '2.0',
    id: requestId,
    result: { decision: 'deny', reason: 'Rejected by user.' },
  })
}

/** Codex approval policy labels (using Codex-native kebab-case values). */
export const CODEX_PERMISSION_MODE_LABELS: Record<string, string> = {
  'never': 'Full Auto',
  'on-request': 'Suggest & Approve',
  'untrusted': 'Auto-edit',
}

/** Returns the default model for the given agent provider. */
export function defaultModelForProvider(provider: number): string {
  // AgentProvider.CODEX = 2
  if (provider === 2)
    return DEFAULT_CODEX_MODEL
  return DEFAULT_MODEL
}

/** Returns the default effort for the given agent provider. */
export function defaultEffortForProvider(provider: number): string {
  if (provider === 2)
    return DEFAULT_CODEX_EFFORT
  return DEFAULT_EFFORT
}
