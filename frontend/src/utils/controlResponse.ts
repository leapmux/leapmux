import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
// Import directly from registry (not providers/index) to avoid circular dependency.
// providers/index re-exports from registry but also side-effect-imports claude/codex,
// which import settingsShared, which imports this module's constants.
import { getProviderPlugin } from '~/components/chat/providers/registry'

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
  permissionMode?: PermissionMode,
  clearContext?: boolean,
): Record<string, unknown> {
  return {
    type: 'control_response',
    permissionMode, // optional, consumed by hub for ExitPlanMode
    ...(clearContext ? { clearContext: true } : {}),
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
export type PermissionMode = string

/** Returns the default model for the given agent provider. */
export function defaultModelForProvider(provider: AgentProvider): string {
  return getProviderPlugin(provider)?.defaultModel ?? 'opus'
}

/** Returns the default effort for the given agent provider. */
export function defaultEffortForProvider(provider: AgentProvider): string {
  return getProviderPlugin(provider)?.defaultEffort ?? 'high'
}
