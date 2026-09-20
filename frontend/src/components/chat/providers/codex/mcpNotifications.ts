import { CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

const NON_FAILURE_STARTUP_STATES = new Set(['starting', 'ready', 'cancelled'])

/** The startup state from one Codex MCP notification, or null for another method. */
function codexMcpStartupState(message: Record<string, unknown>): string | null {
  if (message.method !== CODEX_METHOD.McpServerStartupStatusUpdated)
    return null
  const status = pickObject(message, 'params')?.status
  return typeof status === 'string'
    ? status
    : pickString(isObject(status) ? status : undefined, 'state')
}

/** Whether a startup notification states a failure or an unknown future state. */
export function codexMcpStartupIsFailureOrUnknown(message: Record<string, unknown>): boolean {
  const state = codexMcpStartupState(message)
  return state !== null && !NON_FAILURE_STARTUP_STATES.has(state)
}

/** Whether an OAuth completion states a failure or an unknown future outcome. */
export function codexMcpOauthIsFailureOrUnknown(message: Record<string, unknown>): boolean {
  return message.method === CODEX_METHOD.McpServerOauthLoginCompleted
    && pickObject(message, 'params')?.success !== true
}
