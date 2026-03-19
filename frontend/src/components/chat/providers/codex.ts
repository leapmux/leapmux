import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import {
  EFFORT_LABELS,
} from '~/utils/controlResponse'
import { isObject } from '../messageUtils'
import {
  codexAgentMessageRenderer,
  codexCommandExecutionRenderer,
  codexFileChangeRenderer,
  codexMcpToolCallRenderer,
  codexReasoningRenderer,
  codexTurnCompletedRenderer,
} from './codexRenderers'
import { registerProvider } from './registry'

/** Default model for Codex agents. */
const DEFAULT_CODEX_MODEL = import.meta.env.LEAPMUX_DEFAULT_CODEX_MODEL || 'gpt-5.4'
const DEFAULT_CODEX_EFFORT = 'medium'

const CODEX_MODEL_LABELS: Record<string, string> = {
  'o4-mini': 'o4-mini',
  'o3': 'o3',
  'gpt-5.4': 'GPT-5.4',
  'codex-mini': 'Codex Mini',
}

/** Codex approval policy labels (using Codex-native kebab-case values). */
const CODEX_PERMISSION_MODE_LABELS: Record<string, string> = {
  'never': 'Full Auto',
  'on-request': 'Suggest & Approve',
  'untrusted': 'Auto-edit',
}

let codexReqIdCounter = 1000

/**
 * Builds a JSON-RPC request for interrupting a Codex turn.
 */
function buildCodexInterruptRequest(threadId: string, turnId: string): string {
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
function buildCodexApprovalResponse(requestId: number, approved: boolean, decision?: string): string {
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

/** Check whether the wrapper envelope represents a notification thread. */
function isNotificationThreadWrapper(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (!wrapper || wrapper.messages.length < 1)
    return false
  const first = wrapper.messages[0] as Record<string, unknown>
  const t = first.type as string | undefined
  return t === 'settings_changed' || t === 'context_cleared' || t === 'interrupted'
    || t === 'rate_limit' || t === 'agent_renamed' || t === 'agent_error'
}

const codexPlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CODEX_MODEL,
  defaultEffort: DEFAULT_CODEX_EFFORT,
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isNotificationThreadWrapper(wrapper))
      return { kind: 'notification_thread', messages: wrapper.messages }

    // Empty wrapper — hide.
    if (wrapper && wrapper.messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    // Codex item types from item/completed notifications.
    // The params are stored natively: {item: {type: "agentMessage", ...}, threadId, turnId}
    const item = parent.item as Record<string, unknown> | undefined
    const itemType = item?.type as string | undefined

    // turn/completed → result divider
    if (parent.turn && isObject(parent.turn) && (parent.turn as Record<string, unknown>).status)
      return { kind: 'result_divider' }

    if (item && itemType) {
      // agentMessage → assistant text
      if (itemType === 'agentMessage')
        return { kind: 'assistant_text' }

      // commandExecution → tool use
      if (itemType === 'commandExecution')
        return { kind: 'tool_use', toolName: 'commandExecution', toolUse: item, content: [] }

      // fileChange → tool use
      if (itemType === 'fileChange')
        return { kind: 'tool_use', toolName: 'fileChange', toolUse: item, content: [] }

      // mcpToolCall → tool use
      if (itemType === 'mcpToolCall')
        return { kind: 'tool_use', toolName: (item.tool as string) || 'mcpTool', toolUse: item, content: [] }

      // dynamicToolCall → tool use
      if (itemType === 'dynamicToolCall')
        return { kind: 'tool_use', toolName: (item.tool as string) || 'dynamicTool', toolUse: item, content: [] }

      // reasoning → thinking
      if (itemType === 'reasoning')
        return { kind: 'assistant_thinking' }

      // userMessage → user content
      if (itemType === 'userMessage')
        return { kind: 'user_content' }
    }

    // User message (persisted by LeapMux service layer)
    if (!parent.item && typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      return { kind: 'user_content' }
    }

    // LeapMux notification types
    const type = parent.type as string | undefined
    if (type === 'settings_changed' || type === 'context_cleared'
      || type === 'interrupted' || type === 'agent_error' || type === 'agent_renamed') {
      return { kind: 'notification' }
    }

    return { kind: 'unknown' }
  },

  renderMessage(category: MessageCategory, parsed: unknown, role: MessageRole, context?: RenderContext): JSX.Element | null {
    if (category.kind === 'assistant_text')
      return codexAgentMessageRenderer(parsed, role, context)
    if (category.kind === 'assistant_thinking')
      return codexReasoningRenderer(parsed, role, context)
    if (category.kind === 'result_divider')
      return codexTurnCompletedRenderer(parsed, role, context)
    if (category.kind === 'tool_use') {
      const toolName = (category as { toolName: string }).toolName
      if (toolName === 'commandExecution')
        return codexCommandExecutionRenderer(parsed, role, context)
      if (toolName === 'fileChange')
        return codexFileChangeRenderer(parsed, role, context)
      return codexMcpToolCallRenderer(parsed, role, context)
    }
    return null
  },

  buildInterruptContent(agentSessionId: string, codexTurnId?: string): string | null {
    if (!agentSessionId || !codexTurnId)
      return null
    return buildCodexInterruptRequest(agentSessionId, codexTurnId)
  },

  buildControlResponse(parsed: Record<string, unknown>): Uint8Array | null {
    const requestId = (parsed?.response as Record<string, unknown>)?.request_id as string | undefined
    if (!requestId)
      return null
    const numId = Number(requestId)
    const rpcId = Number.isFinite(numId) ? numId : requestId
    const behavior = ((parsed?.response as Record<string, unknown>)?.response as Record<string, unknown>)?.behavior
    const approved = behavior === 'allow'
    return new TextEncoder().encode(buildCodexApprovalResponse(rpcId as number, approved))
  },

  SettingsPanel: ((props: ProviderSettingsPanelProps) => {
    const models = Object.entries(CODEX_MODEL_LABELS)
    const efforts = Object.entries(EFFORT_LABELS)
    const permModes = Object.entries(CODEX_PERMISSION_MODE_LABELS)

    return [
      // Model selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Model</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of models) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.model || DEFAULT_CODEX_MODEL))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onModelChange?.(select.value))
        el.appendChild(select)
        return el
      })(),
      // Effort selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Reasoning Effort</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of efforts) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.effort || DEFAULT_CODEX_EFFORT))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onEffortChange?.(select.value))
        el.appendChild(select)
        return el
      })(),
      // Permission mode selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Approval Policy</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of permModes) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.permissionMode || 'never'))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onPermissionModeChange?.(select.value as PermissionMode))
        el.appendChild(select)
        return el
      })(),
    ] as unknown as JSX.Element
  }) as unknown as ProviderPlugin['SettingsPanel'],

  settingsTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
    const model = props.model || DEFAULT_CODEX_MODEL
    const label = CODEX_MODEL_LABELS[model] || model
    return label as unknown as JSX.Element
  },
}

registerProvider(AgentProvider.CODEX, codexPlugin)
