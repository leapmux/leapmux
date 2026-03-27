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
import * as styles from '../ChatView.css'
import {
  codexAgentMessageRenderer,
  codexCollabAgentToolCallRenderer,
  codexCommandExecutionRenderer,
  codexFileChangeRenderer,
  codexMcpToolCallRenderer,
  codexPlanRenderer,
  codexReasoningRenderer,
  codexTurnCompletedRenderer,
  codexTurnPlanRenderer,
  codexWebSearchRenderer,
} from '../codexRenderers'
import { CodexControlActions, CodexControlContent } from '../controls/CodexControlRequest'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { defaultModelId, effortItems, hasEfforts, modeLabel, modelDisplayName, modelItems, ModelSelect, optionGroup, optionGroupItems, optionLabel, permissionModeGroup, permissionModeItems, RadioGroup } from '../settingsShared'
import { registerProvider } from './registry'

/** Default model for Codex agents. */
const DEFAULT_CODEX_MODEL = import.meta.env.LEAPMUX_CODEX_DEFAULT_MODEL || 'gpt-5.4'
const DEFAULT_CODEX_EFFORT = 'high'
export const DEFAULT_CODEX_COLLABORATION_MODE = 'default'
export const DEFAULT_CODEX_SANDBOX_POLICY = 'workspace-write'
export const DEFAULT_CODEX_NETWORK_ACCESS = 'restricted'
export const DEFAULT_CODEX_SERVICE_TIER = 'default'
export const CODEX_EXTRA_COLLABORATION_MODE = 'collaboration_mode'
export const CODEX_EXTRA_SANDBOX_POLICY = 'sandbox_policy'
export const CODEX_EXTRA_NETWORK_ACCESS = 'network_access'
export const CODEX_EXTRA_SERVICE_TIER = 'service_tier'

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

function isCodexInterruptRequestText(content: string): boolean {
  try {
    const parsed = JSON.parse(content) as Record<string, unknown>
    return parsed.method === 'turn/interrupt'
  }
  catch {
    return false
  }
}

function codexAssistantInterruptEcho(parent: Record<string, unknown>): boolean {
  if (parent.role !== 'assistant')
    return false

  if (typeof parent.content === 'string')
    return isCodexInterruptRequestText(parent.content)

  if (parent.type !== 'assistant' || !isObject(parent.message))
    return false

  const content = (parent.message as Record<string, unknown>).content
  if (!Array.isArray(content))
    return false

  const text = content
    .filter((c: unknown) => isObject(c) && c.type === 'text')
    .map((c: unknown) => String((c as Record<string, unknown>).text || ''))
    .join('')

  return text.length > 0 && isCodexInterruptRequestText(text)
}

function isCodexJsonRpcResponse(parent: Record<string, unknown>): boolean {
  if ('method' in parent || 'item' in parent || 'turn' in parent)
    return false
  return ('result' in parent || 'error' in parent) && ('id' in parent)
}

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set(['agent_error'])
function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')) {
    return true
  }
  // Codex method-based notifications (e.g. account/rateLimits/updated)
  if (!wrapper || wrapper.messages.length < 1)
    return false
  return wrapper.messages.some(msg =>
    isObject(msg) && msg.method === 'account/rateLimits/updated')
}

/** Returns true when a Codex rate limit message has all tiers below the warning threshold. */
function isCodexRateLimitAllAllowed(m: Record<string, unknown>): boolean {
  if (m.method !== 'account/rateLimits/updated')
    return false
  const params = m.params as Record<string, unknown> | undefined
  const rl = params?.rateLimits as Record<string, unknown> | undefined
  if (!rl)
    return true
  for (const tierKey of ['primary', 'secondary']) {
    const tier = rl[tierKey] as Record<string, unknown> | undefined
    if (tier && (tier.usedPercent as number) >= 80)
      return false
  }
  return true
}

function isCodexHiddenNotificationThreadMessage(m: unknown): boolean {
  if (!isObject(m))
    return false
  const msg = m as Record<string, unknown>
  if (msg.method === 'thread/tokenUsage/updated')
    return true
  return isCodexRateLimitAllAllowed(msg)
}

/** Codex settings panel (model, effort, approval policy, sandbox). */
function CodexSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const extra = () => props.extraSettings || {}
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => extra()[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE
  const currentSandbox = () => extra()[CODEX_EXTRA_SANDBOX_POLICY] || DEFAULT_CODEX_SANDBOX_POLICY
  const currentNetwork = () => extra()[CODEX_EXTRA_NETWORK_ACCESS] || DEFAULT_CODEX_NETWORK_ACCESS
  const currentServiceTier = () => extra()[CODEX_EXTRA_SERVICE_TIER] || DEFAULT_CODEX_SERVICE_TIER

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const serviceTierGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_SERVICE_TIER)
  const serviceTierItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_SERVICE_TIER)
  const collaborationModeGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE)
  const collaborationModeItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE)
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)
  const sandboxGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_SANDBOX_POLICY)
  const sandboxItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_SANDBOX_POLICY)
  const networkGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_NETWORK_ACCESS)
  const networkItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_NETWORK_ACCESS)

  // Identify the first visible group in each column so settingsFieldsetFirst
  // is applied only to it. Using a derived signal avoids fragile cascading
  // conditionals that must be updated every time a new group is added.
  const leftFirstGroup = () => serviceTierItems().length > 0 ? 'tier' : 'effort'
  const rightFirstGroup = () =>
    collaborationModeItems().length > 0
      ? 'collab'
      : networkItems().length > 0
        ? 'network'
        : sandboxItems().length > 0
          ? 'sandbox'
          : 'mode'
  const firstLeftClass = (id: string) => leftFirstGroup() === id ? styles.settingsFieldsetFirst : undefined
  const firstRightClass = (id: string) => rightFirstGroup() === id ? styles.settingsFieldsetFirst : undefined

  return (
    <div class={styles.settingsPanelColumns}>
      <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
        <Show when={serviceTierItems().length > 0}>
          <RadioGroup
            label={serviceTierGroup()?.label || 'Fast Mode'}
            items={serviceTierItems()}
            testIdPrefix="codex-service-tier"
            name={`${menuId}-service-tier`}
            current={currentServiceTier()}
            onChange={v => props.onOptionGroupChange?.(CODEX_EXTRA_SERVICE_TIER, v)}
            fieldsetClass={firstLeftClass('tier')}
          />
        </Show>
        <RadioGroup
          label="Reasoning Effort"
          items={efforts()}
          testIdPrefix="effort"
          name={`${menuId}-effort`}
          current={currentEffort()}
          onChange={v => props.onEffortChange?.(v)}
          fieldsetClass={firstLeftClass('effort')}
        />
        <ModelSelect
          items={models()}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={v => props.onModelChange?.(v)}
        />
      </div>
      <div class={styles.settingsPanelColumn}>
        <Show when={collaborationModeItems().length > 0}>
          <RadioGroup
            label={collaborationModeGroup()?.label || 'Workflow'}
            items={collaborationModeItems()}
            testIdPrefix="codex-collaboration-mode"
            name={`${menuId}-collaboration-mode`}
            current={currentCollaborationMode()}
            onChange={v => props.onOptionGroupChange?.(CODEX_EXTRA_COLLABORATION_MODE, v)}
            fieldsetClass={firstRightClass('collab')}
          />
        </Show>
        <Show when={networkItems().length > 0}>
          <RadioGroup
            label={networkGroup()?.label || 'Network Access'}
            items={networkItems()}
            testIdPrefix="network"
            name={`${menuId}-network`}
            current={currentNetwork()}
            onChange={v => props.onOptionGroupChange?.(CODEX_EXTRA_NETWORK_ACCESS, v)}
            fieldsetClass={firstRightClass('network')}
          />
        </Show>
        <Show when={sandboxItems().length > 0}>
          <RadioGroup
            label={sandboxGroup()?.label || 'Sandbox'}
            items={sandboxItems()}
            testIdPrefix="sandbox"
            name={`${menuId}-sandbox`}
            current={currentSandbox()}
            onChange={v => props.onOptionGroupChange?.(CODEX_EXTRA_SANDBOX_POLICY, v)}
            fieldsetClass={firstRightClass('sandbox')}
          />
        </Show>
        <div>
          <RadioGroup
            label={modeGroup()?.label || 'Approval Policy'}
            items={modeItems()}
            testIdPrefix="permission-mode"
            name={`${menuId}-mode`}
            current={currentMode()}
            onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
            fieldsetClass={firstRightClass('mode')}
          />
        </div>
      </div>
    </div>
  )
}

/** Codex trigger label (model name, effort icon, current mode). */
function CodexTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const extra = () => props.extraSettings || {}
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => extra()[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE
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
  const mode = () => currentCollaborationMode() === 'plan'
    ? optionLabel(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE, currentCollaborationMode())
    : modeLabel(props.availableOptionGroups, currentMode())
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
  defaultPermissionMode: 'on-request',
  bypassPermissionMode: 'never',
  attachments: {
    text: true,
    image: true,
    pdf: false,
    binary: false,
  },
  planMode: {
    currentMode: agent => agent.extraSettings?.[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE,
    planValue: 'plan',
    defaultValue: DEFAULT_CODEX_COLLABORATION_MODE,
    setMode: (mode, cb) => cb.onOptionGroupChange?.(CODEX_EXTRA_COLLABORATION_MODE, mode),
  },
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper)) {
      // Filter notifications that are intentionally invisible in chat.
      const msgs = wrapper.messages.filter(m => !isCodexHiddenNotificationThreadMessage(m))
      if (msgs.length === 0)
        return { kind: 'hidden' }
      return { kind: 'notification_thread', messages: msgs }
    }

    // Empty wrapper — hide.
    if (wrapper && wrapper.messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    if (isCodexJsonRpcResponse(parent))
      return { kind: 'hidden' }

    if (codexAssistantInterruptEcho(parent))
      return { kind: 'hidden' }

    const type = parent.type as string | undefined
    const subtype = parent.subtype as string | undefined

    // Startup and status notifications are transient lifecycle signals.
    // Persist them if needed, but keep them out of chat rendering.
    if (parent.method === 'thread/started' || parent.method === 'turn/started' || parent.method === 'thread/status/changed')
      return { kind: 'hidden' }

    if (type === 'system') {
      if (subtype === 'init')
        return { kind: 'hidden' }
      if (subtype === 'status' && parent.status !== 'compacting')
        return { kind: 'hidden' }
      if (subtype === 'task_notification')
        return { kind: 'hidden' }
      return { kind: 'notification' }
    }

    if (parent.method === 'turn/plan/updated' && isObject(parent.params))
      return { kind: 'tool_use', toolName: 'turnPlan', toolUse: parent, content: [] }

    // Each item is now its own message (no more merging).
    const effective = parent

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

      // plan → tool_use (rendered bubble-less like ExitPlanMode)
      if (itemType === 'plan')
        return { kind: 'tool_use', toolName: 'plan', toolUse: item, content: [] }

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

      // collabAgentToolCall → tool use (SpawnAgent)
      if (itemType === 'collabAgentToolCall') {
        if (item.tool === 'spawnAgent' && item.status === 'completed')
          return { kind: 'hidden' }
        return { kind: 'tool_use', toolName: 'collabAgentToolCall', toolUse: item, content: [] }
      }

      // webSearch → tool use / result-like native codex message
      if (itemType === 'webSearch')
        return { kind: 'tool_use', toolName: 'webSearch', toolUse: item, content: [] }

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

    // Codex method-based notifications
    if (parent.method === 'thread/tokenUsage/updated')
      return { kind: 'hidden' }

    if (parent.method === 'account/rateLimits/updated') {
      if (isCodexRateLimitAllAllowed(parent))
        return { kind: 'hidden' }
      return { kind: 'notification' }
    }

    // LeapMux notification types
    if (type === 'settings_changed' || type === 'context_cleared'
      || type === 'interrupted' || type === 'agent_error' || type === 'agent_renamed' || type === 'compacting') {
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
      if (cat.toolName === 'turnPlan')
        return codexTurnPlanRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'plan')
        return codexPlanRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'commandExecution')
        return codexCommandExecutionRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'fileChange')
        return codexFileChangeRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'webSearch')
        return codexWebSearchRenderer(cat.toolUse, role, context)
      if (cat.toolName === 'collabAgentToolCall')
        return codexCollabAgentToolCallRenderer(cat.toolUse, role, context)
      return codexMcpToolCallRenderer(cat.toolUse, role, context)
    }
    return null
  },

  buildInterruptContent(agentSessionId: string, codexTurnId?: string): string | null {
    if (!agentSessionId || !codexTurnId)
      return null
    return buildCodexInterruptRequest(agentSessionId, codexTurnId)
  },

  isAskUserQuestion(payload) {
    return (payload as Record<string, unknown>).method === 'item/tool/requestUserInput'
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
  settingsMenuClass: styles.settingsMenuWide,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
