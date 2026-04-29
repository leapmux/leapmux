import type { MessageCategory } from '../../messageClassification'
import type { ClassificationContext, ClassificationInput } from '../registry'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { PermissionMode } from '~/utils/controlResponse'
import * as workerRpc from '~/api/workerRpc'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { extractAgentText } from './renderers/helpers'

export function buildACPInterruptContent(agentSessionId: string): string | null {
  if (!agentSessionId)
    return null
  return JSON.stringify({
    jsonrpc: '2.0',
    method: 'session/cancel',
    params: { sessionId: agentSessionId },
  })
}

/**
 * Build the wire-format control-response for an ACP-style control request.
 * Deny when the user provided text feedback, allow otherwise (echoing back
 * the original tool input). Used by every ACP-based provider plugin
 * (`buildControlResponse: acpBuildControlResponse`).
 */
export function acpBuildControlResponse(
  payload: Record<string, unknown>,
  content: string,
  requestId: string,
): Record<string, unknown> {
  return content
    ? buildDenyResponse(requestId, content)
    : buildAllowResponse(requestId, getToolInput(payload))
}

export async function changeACPPermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
  await workerRpc.updateAgentSettings(workerId, {
    agentId,
    settings: { permissionMode: mode },
  })
}

const ACP_EXTRA_NOTIF_TYPES = new Set(['agent_error'])

export function isACPNotifThread(wrapper: { messages: unknown[] } | null): boolean {
  return isNotificationThreadWrapper(wrapper, ACP_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

/**
 * True when `parent` is a JSON-RPC response envelope (a `result`/`error`
 * payload with an `id` and no `method`). Shared by Codex and ACP-based
 * provider classifiers, which all hide these from the chat view.
 */
export function isJsonRpcResponseObject(parent: Record<string, unknown>): boolean {
  if ('method' in parent)
    return false
  return ('result' in parent || 'error' in parent) && ('id' in parent)
}

export interface ACPClassifyConfig {
  extraHiddenSessionUpdates?: Set<string>
}

/**
 * Shared `extractQuotableText` for ACP-based providers (OpenCode, Cursor,
 * Kilo, Goose, Copilot, Gemini). Reads `parent.content.text` for
 * agent_message_chunk / agent_thought_chunk shapes (via `extractAgentText`)
 * and falls back to plain string `parent.content` for user_content /
 * plan_execution.
 */
export function acpExtractQuotableText(category: MessageCategory, parsed: ParsedMessageContent): string | null {
  const obj = parsed.parentObject
  if (!obj)
    return null
  if (category.kind === 'assistant_text' || category.kind === 'assistant_thinking')
    return extractAgentText(obj).trim() || null
  if (category.kind === 'user_content' || category.kind === 'plan_execution') {
    if (typeof obj.content === 'string')
      return (obj.content as string).trim() || null
  }
  return null
}

export function classifyACPMessage(config: ACPClassifyConfig = {}): (input: ClassificationInput, context?: ClassificationContext) => MessageCategory {
  const baseHidden = new Set<string>([
    ACP_SESSION_UPDATE.USAGE_UPDATE,
    ACP_SESSION_UPDATE.AVAILABLE_COMMANDS_UPDATE,
    ACP_SESSION_UPDATE.USER_MESSAGE_CHUNK,
  ])
  const hiddenSessionUpdates = config.extraHiddenSessionUpdates
    ? new Set([...baseHidden, ...config.extraHiddenSessionUpdates])
    : baseHidden
  return (input: ClassificationInput, _context?: ClassificationContext): MessageCategory => {
    const parent = input.parentObject
    const wrapper = input.wrapper

    if (wrapper) {
      if (isACPNotifThread(wrapper))
        return { kind: 'notification_thread', messages: wrapper.messages }
      if (wrapper.messages.length === 0)
        return { kind: 'hidden' }
    }

    if (!parent)
      return { kind: 'unknown' }

    const sessionUpdate = parent.sessionUpdate as string | undefined
    const type = parent.type as string | undefined
    const subtype = parent.subtype as string | undefined

    if (sessionUpdate === ACP_SESSION_UPDATE.AGENT_MESSAGE_CHUNK)
      return { kind: 'assistant_text' }

    if (sessionUpdate === ACP_SESSION_UPDATE.AGENT_THOUGHT_CHUNK)
      return { kind: 'assistant_thinking' }

    if (sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL)
      return { kind: 'tool_use', toolName: (parent.kind as string) || ACP_SESSION_UPDATE.TOOL_CALL, toolUse: parent, content: [] }

    if (sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE) {
      const status = parent.status as string | undefined
      if (status === 'completed' || status === 'failed' || status === 'cancelled')
        return { kind: 'tool_use', toolName: (parent.kind as string) || ACP_SESSION_UPDATE.TOOL_CALL_UPDATE, toolUse: parent, content: [] }
      return { kind: 'hidden' }
    }

    if (sessionUpdate === ACP_SESSION_UPDATE.PLAN)
      return { kind: 'tool_use', toolName: ACP_SESSION_UPDATE.PLAN, toolUse: parent, content: [] }

    if (hiddenSessionUpdates.has(sessionUpdate!))
      return { kind: 'hidden' }

    if (parent.stopReason !== undefined)
      return { kind: 'result_divider' }

    if (type === 'system') {
      if (subtype === 'init' || subtype === 'task_notification')
        return { kind: 'hidden' }
      return { kind: 'notification' }
    }

    if (type === 'settings_changed' || type === 'context_cleared'
      || type === 'interrupted' || type === 'agent_error' || type === 'plan_updated' || type === 'compacting') {
      return { kind: 'notification' }
    }

    if (!sessionUpdate && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }

    if (isJsonRpcResponseObject(parent))
      return { kind: 'hidden' }

    return { kind: 'unknown' }
  }
}
