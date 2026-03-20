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
import { CodexControlActions, CodexControlContent } from '../controls/CodexControlRequest'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { effortItems, hasEfforts, modeLabel, modelDisplayName, modelItems, permissionModeGroup, permissionModeItems, RadioGroup } from '../settingsShared'
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
 * Builds a JSON-RPC response for a Codex approval request.
 */
function buildCodexApprovalResponse(requestId: number | string, decision: Record<string, unknown> | string): string {
  return JSON.stringify({
    jsonrpc: '2.0',
    id: requestId,
    result: { decision },
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
  const currentNetwork = () => props.codexNetworkAccess || 'restricted'

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)

  const optionGroupItems = (key: string) => {
    const group = props.availableOptionGroups?.find(g => g.key === key)
    if (group && group.options.length > 0)
      return { label: group.label, items: group.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined })) }
    return null
  }

  const sandbox = () => optionGroupItems('codexSandboxPolicy')
  const network = () => optionGroupItems('codexNetworkAccess')

  return (
    <>
      <RadioGroup
        label="Model"
        items={models()}
        testIdPrefix="model"
        name={`${menuId}-model`}
        current={currentModel()}
        onChange={v => props.onModelChange?.(v)}
      />
      <RadioGroup
        label="Reasoning Effort"
        items={efforts()}
        testIdPrefix="effort"
        name={`${menuId}-effort`}
        current={currentEffort()}
        onChange={v => props.onEffortChange?.(v)}
      />
      <RadioGroup
        label={modeGroup()?.label || 'Approval Policy'}
        items={modeItems()}
        testIdPrefix="permission-mode"
        name={`${menuId}-mode`}
        current={currentMode()}
        onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
      />
      <Show when={sandbox()}>
        <RadioGroup
          label={sandbox()!.label || 'Sandbox'}
          items={sandbox()!.items}
          testIdPrefix="sandbox"
          name={`${menuId}-sandbox`}
          current={currentSandbox()}
          onChange={v => props.onCodexSandboxPolicyChange?.(v)}
        />
      </Show>
      <Show when={network()}>
        <RadioGroup
          label={network()!.label || 'Network Access'}
          items={network()!.items}
          testIdPrefix="network"
          name={`${menuId}-network`}
          current={currentNetwork()}
          onChange={v => props.onCodexNetworkAccessChange?.(v)}
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
  const displayName = () => modelDisplayName(props.availableModels, currentModel())

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

  const hasEffort = () => hasEfforts(props.availableModels, currentModel())
  const mode = () => modeLabel(props.availableOptionGroups, currentMode())
  return (
    <>
      {displayName()}
      <Show when={hasEffort()}>{effortIcon()}</Show>
      {' '}
      {mode()}
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

    // thread/status/changed notifications are transient status signals
    // (e.g. waitingOnApproval). Persisted but not displayed.
    if (parent.method === 'thread/status/changed')
      return { kind: 'hidden' }

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
    const response = parsed?.response as Record<string, unknown> | undefined
    const requestId = response?.request_id as string | undefined
    if (!requestId)
      return null
    const numId = Number(requestId)
    const rpcId = Number.isFinite(numId) ? numId : requestId
    // Provider-specific decision (set by Codex ControlActions) — pass through as-is.
    const inner = response?.response as Record<string, unknown> | undefined
    const codexDecision = inner?.codexDecision as Record<string, unknown> | string | undefined
    if (codexDecision)
      return new TextEncoder().encode(buildCodexApprovalResponse(rpcId, codexDecision))
    // Generic allow/deny from GenericToolActions — translate to accept/decline.
    const behavior = inner?.behavior
    return new TextEncoder().encode(buildCodexApprovalResponse(rpcId, behavior === 'allow' ? 'accept' : 'decline'))
  },

  // Codex applies the new approval policy on the next turn/start.
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { permissionMode: mode },
    })
  },

  ControlContent: CodexControlContent,
  ControlActions: CodexControlActions,

  SettingsPanel: CodexSettingsPanel,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
