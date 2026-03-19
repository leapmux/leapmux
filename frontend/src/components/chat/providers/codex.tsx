import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import {
  EFFORT_LABELS,
} from '~/utils/controlResponse'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { RadioGroup } from '../settingsShared'
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

const CODEX_MODELS = Object.entries(CODEX_MODEL_LABELS).map(([value, label]) => ({ label, value }))
const CODEX_EFFORTS = Object.entries(EFFORT_LABELS).map(([value, label]) => ({ label, value }))

/** Codex approval policy labels (using Codex-native kebab-case values). */
const CODEX_PERMISSION_MODE_LABELS: Record<string, string> = {
  'never': 'Full Auto',
  'on-request': 'Suggest & Approve',
  'untrusted': 'Auto-edit',
}

const CODEX_PERMISSION_MODES = Object.entries(CODEX_PERMISSION_MODE_LABELS).map(([value, label]) => ({ label, value }))

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

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set(['agent_error'])
function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES)
}

/** Codex settings panel (model, effort, approval policy). */
function CodexSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'never'

  return (
    <>
      <RadioGroup
        label="Model"
        items={CODEX_MODELS}
        testIdPrefix="model"
        name={`${menuId}-model`}
        current={currentModel()}
        onChange={v => props.onModelChange?.(v)}
      />
      <RadioGroup
        label="Reasoning Effort"
        items={CODEX_EFFORTS}
        testIdPrefix="effort"
        name={`${menuId}-effort`}
        current={currentEffort()}
        onChange={v => props.onEffortChange?.(v)}
      />
      <RadioGroup
        label="Approval Policy"
        items={CODEX_PERMISSION_MODES}
        testIdPrefix="permission-mode"
        name={`${menuId}-mode`}
        current={currentMode()}
        onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
      />
    </>
  )
}

/** Codex trigger label (model name). */
function CodexTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || DEFAULT_CODEX_MODEL
  return <>{CODEX_MODEL_LABELS[currentModel()] || currentModel()}</>
}

const codexPlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CODEX_MODEL,
  defaultEffort: DEFAULT_CODEX_EFFORT,
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper))
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

  // Codex sets approvalPolicy at thread/start time — changing it requires
  // a restart via UpdateAgentSettings (which resumes the thread).
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      model: '',
      effort: '',
      permissionMode: mode,
    })
  },

  SettingsPanel: CodexSettingsPanel,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
