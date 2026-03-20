import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Icon } from '~/components/common/Icon'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import {
  codexAgentMessageRenderer,
  codexCommandExecutionRenderer,
  codexFileChangeRenderer,
  codexMcpToolCallRenderer,
  codexReasoningRenderer,
  codexTurnCompletedRenderer,
} from '../codexRenderers'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { RadioGroup } from '../settingsShared'
import { registerProvider } from './registry'

/** Default model for Codex agents. */
const DEFAULT_CODEX_MODEL = import.meta.env.LEAPMUX_CODEX_DEFAULT_MODEL || 'gpt-5.4'
const DEFAULT_CODEX_EFFORT = 'medium'

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
      result: { decision: decision || 'approved' },
    })
  }
  return JSON.stringify({
    jsonrpc: '2.0',
    id: requestId,
    result: { decision: 'denied', reason: 'Rejected by user.' },
  })
}

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set(['agent_error'])
function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES)
}

/** Codex settings panel (model, effort, approval policy, sandbox). */
function CodexSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentSandbox = () => props.codexSandboxPolicy || 'workspace-write'

  const modelItems = () => {
    const models = props.availableModels
    if (models && models.length > 0)
      return models.map(m => ({ label: m.displayName || m.id, value: m.id, tooltip: m.description || undefined }))
    return []
  }

  const effortItems = () => {
    const models = props.availableModels
    if (models && models.length > 0) {
      const model = models.find(m => m.id === currentModel())
      if (model)
        return model.supportedEfforts.map(e => ({ label: e.name || e.id, value: e.id, tooltip: e.description || undefined }))
    }
    return []
  }

  const permissionModeGroup = () =>
    props.availableOptionGroups?.find(g => g.key === 'permissionMode')

  const permissionModeItems = () => {
    const group = permissionModeGroup()
    if (group && group.options.length > 0)
      return group.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined }))
    return []
  }

  const sandboxGroup = () =>
    props.availableOptionGroups?.find(g => g.key === 'codexSandboxPolicy')

  const sandboxItems = () => {
    const group = sandboxGroup()
    if (group && group.options.length > 0)
      return group.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined }))
    return []
  }

  return (
    <>
      <RadioGroup
        label="Model"
        items={modelItems()}
        testIdPrefix="model"
        name={`${menuId}-model`}
        current={currentModel()}
        onChange={v => props.onModelChange?.(v)}
      />
      <RadioGroup
        label="Reasoning Effort"
        items={effortItems()}
        testIdPrefix="effort"
        name={`${menuId}-effort`}
        current={currentEffort()}
        onChange={v => props.onEffortChange?.(v)}
      />
      <RadioGroup
        label={permissionModeGroup()?.label || 'Approval Policy'}
        items={permissionModeItems()}
        testIdPrefix="permission-mode"
        name={`${menuId}-mode`}
        current={currentMode()}
        onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
      />
      <Show when={sandboxItems().length > 0}>
        <RadioGroup
          label={sandboxGroup()?.label || 'Sandbox'}
          items={sandboxItems()}
          testIdPrefix="sandbox"
          name={`${menuId}-sandbox`}
          current={currentSandbox()}
          onChange={v => props.onCodexSandboxPolicyChange?.(v)}
        />
      </Show>
    </>
  )
}

/** Codex trigger label (model name, effort icon, approval policy). */
function CodexTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const displayName = () => {
    const models = props.availableModels
    if (models && models.length > 0) {
      const model = models.find(m => m.id === currentModel())
      if (model)
        return model.displayName || model.id
    }
    return currentModel()
  }

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'xhigh': return <Icon icon={Zap} size="xs" />
      case 'high': return <Icon icon={ChevronsUp} size="xs" />
      case 'low': return <Icon icon={ChevronsDown} size="xs" />
      case 'minimal': return <Icon icon={ChevronsDown} size="xs" />
      case 'none': return <Icon icon={ChevronsDown} size="xs" />
      default: return <Icon icon={Dot} size="xs" />
    }
  }

  const hasEfforts = () => {
    const models = props.availableModels
    if (models && models.length > 0) {
      const model = models.find(m => m.id === currentModel())
      return model ? model.supportedEfforts.length > 0 : false
    }
    return false
  }

  const modeLabel = () => {
    const group = props.availableOptionGroups?.find(g => g.key === 'permissionMode')
    if (group) {
      const opt = group.options.find(o => o.id === currentMode())
      if (opt)
        return opt.name || opt.id
    }
    return currentMode()
  }
  return (
    <>
      {displayName()}
      <Show when={hasEfforts()}>{effortIcon()}</Show>
      {' '}
      {modeLabel()}
    </>
  )
}

const codexPlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CODEX_MODEL,
  defaultEffort: DEFAULT_CODEX_EFFORT,
  bypassPermissionMode: 'never',
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper))
      return { kind: 'notification_thread', messages: wrapper.messages }

    // Empty wrapper — hide.
    if (wrapper && wrapper.messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    // Codex wrapper messages represent state updates of the same item
    // (e.g. inProgress → completed). Use the last message as the effective parent.
    const effective = (wrapper && wrapper.messages.length > 1)
      ? wrapper.messages.at(-1) as Record<string, unknown>
      : parent

    // Codex item types from item/completed notifications.
    // The params are stored natively: {item: {type: "agentMessage", ...}, threadId, turnId}
    const item = (effective.item as Record<string, unknown> | undefined)
      ?? (parent.item as Record<string, unknown> | undefined)
    const itemType = item?.type as string | undefined

    // turn/completed → result divider
    if (effective.turn && isObject(effective.turn) && (effective.turn as Record<string, unknown>).status)
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

      // reasoning → thinking (hide if both summary and content are empty)
      if (itemType === 'reasoning') {
        const summary = item.summary as unknown[] | undefined
        const content = item.content as unknown[] | undefined
        if ((!summary || summary.length === 0) && (!content || content.length === 0))
          return { kind: 'hidden' }
        return { kind: 'assistant_thinking' }
      }

      // userMessage → hidden (echoed back by Codex; persisted but not displayed)
      if (itemType === 'userMessage')
        return { kind: 'hidden' }
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
      // Use the item stored in category.toolUse (resolved to final state in classify).
      const cat = category as { toolName: string, toolUse: Record<string, unknown> }
      if (cat.toolName === 'commandExecution')
        return codexCommandExecutionRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'fileChange')
        return codexFileChangeRenderer(cat.toolUse, role, context)
      return codexMcpToolCallRenderer(cat.toolUse, role, context)
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

  // Codex applies the new approval policy on the next turn/start.
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { permissionMode: mode },
    })
  },

  SettingsPanel: CodexSettingsPanel,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
