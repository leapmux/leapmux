import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
// Import directly from registry (not providers/index) to avoid circular dependency.
// providers/index re-exports from registry but also side-effect-imports claude/codex,
// which import settingsShared, which imports this module's constants.
import { providerFor } from '~/components/chat/providers/registry'

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
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(new TextDecoder().decode(content)) as Record<string, unknown>
  }
  catch {
    return null
  }
  const outer = parsed.response as Record<string, unknown> | undefined
  const inner = outer?.response as Record<string, unknown> | undefined
  const behavior = inner?.behavior
  if (behavior === 'allow' || behavior === 'deny')
    return behavior
  return null
}

/**
 * Builds a control_request JSON string for changing Claude Code's permission mode.
 * The hub detects this format and sends it as raw input to Claude Code's stdin.
 * Uses the same wire protocol as the Agent SDK's setPermissionMode().
 */
export type PermissionMode = string

/** Returns the default model for the given agent provider. */
export function defaultModelForProvider(provider: AgentProvider): string {
  return providerFor(provider)?.defaultModel ?? 'opus'
}

/**
 * Leapmux-side sentinel meaning "let the CLI pick its own default reasoning
 * effort". The backend omits --effort (Claude) / reasoning_effort (Codex)
 * when an agent carries this value, so older CLIs that don't recognize
 * newer effort names (e.g. "xhigh") still work. Mirrors `agent.EffortAuto`
 * in the Go worker.
 */
export const EFFORT_AUTO = 'auto'

/** Returns the default effort for the given agent provider. */
export function defaultEffortForProvider(provider: AgentProvider): string {
  return providerFor(provider)?.defaultEffort ?? EFFORT_AUTO
}
