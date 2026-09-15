import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'

/**
 * Copilot's persisted rows, for the cross-provider parity tests.
 *
 * Copilot speaks its own native protocol, so a row is the whole JSON-RPC frame the
 * runtime sent. Every parity test that feeds each provider its own wire shape builds
 * Copilot's here, so one change to that shape reaches all of them.
 */
export function copilotFrame(type: string, data: Record<string, unknown>, agentId?: string): Record<string, unknown> {
  const event: Record<string, unknown> = { id: `${type}-1`, type, data }
  if (agentId)
    event.agentId = agentId
  return { jsonrpc: '2.0', method: 'session.event', params: { sessionId: 'session-1', event } }
}

/** The `tool.execution_start` row that opens one tool call. */
export function copilotToolStart(toolCallId: string, toolName: string, args: Record<string, unknown>): Record<string, unknown> {
  return copilotFrame(COPILOT_EVENT.ToolStarted, { toolCallId, toolName, arguments: args })
}

/** The `tool.execution_complete` row that closes one tool call. */
export function copilotToolComplete(
  toolCallId: string,
  outcome: { success?: boolean, result?: Record<string, unknown>, error?: Record<string, unknown> },
): Record<string, unknown> {
  return copilotFrame(COPILOT_EVENT.ToolCompleted, { toolCallId, success: outcome.success ?? true, ...outcome.result ? { result: outcome.result } : {}, ...outcome.error ? { error: outcome.error } : {} })
}

/** One pending permission request, as the control surface receives it. */
export function copilotPermissionRequest(request: Record<string, unknown>, requestId = 'native-request'): Record<string, unknown> {
  return copilotFrame(COPILOT_EVENT.PermissionRequested, { requestId, permissionRequest: request })
}
