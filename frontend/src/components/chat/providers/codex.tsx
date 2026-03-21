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
  codexCommandExecutionRenderer,
  codexFileChangeRenderer,
  codexMcpToolCallRenderer,
  codexPlanRenderer,
  codexReasoningRenderer,
  codexTurnCompletedRenderer,
} from '../codexRenderers'
import { CodexControlActions, CodexControlContent } from '../controls/CodexControlRequest'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { defaultModelId, effortItems, hasEfforts, modeLabel, modelDisplayName, modelItems, optionGroup, optionGroupItems, optionLabel, permissionModeGroup, permissionModeItems, RadioGroup } from '../settingsShared'
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

/** Extra notification types for Codex (agent_error). */
const CODEX_EXTRA_NOTIF_TYPES = new Set(['agent_error'])
function isCodexNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (isNotificationThreadWrapper(wrapper, CODEX_EXTRA_NOTIF_TYPES))
    return true
  // Codex method-based notifications (e.g. account/rateLimits/updated)
  if (!wrapper || wrapper.messages.length < 1)
    return false
  const first = wrapper.messages[0] as Record<string, unknown>
  return first.method === 'account/rateLimits/updated'
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

/** Codex settings panel (model, effort, approval policy, sandbox). */
function CodexSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => props.codexCollaborationMode || 'default'
  const currentSandbox = () => props.codexSandboxPolicy || 'workspace-write'
  const currentNetwork = () => props.codexNetworkAccess || 'restricted'

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const collaborationModeGroup = () => optionGroup(props.availableOptionGroups, 'codexCollaborationMode')
  const collaborationModeItems = () => optionGroupItems(props.availableOptionGroups, 'codexCollaborationMode')
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)
  const sandboxGroup = () => optionGroup(props.availableOptionGroups, 'codexSandboxPolicy')
  const sandboxItems = () => optionGroupItems(props.availableOptionGroups, 'codexSandboxPolicy')
  const networkGroup = () => optionGroup(props.availableOptionGroups, 'codexNetworkAccess')
  const networkItems = () => optionGroupItems(props.availableOptionGroups, 'codexNetworkAccess')

  return (
    <div class={styles.settingsPanelColumns}>
      <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
        <RadioGroup
          label="Reasoning Effort"
          items={efforts()}
          testIdPrefix="effort"
          name={`${menuId}-effort`}
          current={currentEffort()}
          onChange={v => props.onEffortChange?.(v)}
          fieldsetClass={styles.settingsFieldsetFirst}
        />
        <RadioGroup
          label="Model"
          items={models()}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={v => props.onModelChange?.(v)}
        />
      </div>
      <div class={styles.settingsPanelColumn}>
        <Show when={collaborationModeItems().length > 0}>
          <div>
            <RadioGroup
              label={collaborationModeGroup()?.label || 'Mode'}
              items={collaborationModeItems()}
              testIdPrefix="codex-collaboration-mode"
              name={`${menuId}-collaboration-mode`}
              current={currentCollaborationMode()}
              onChange={v => props.onCodexCollaborationModeChange?.(v)}
              fieldsetClass={styles.settingsFieldsetFirst}
            />
          </div>
        </Show>
        <Show when={networkItems().length > 0}>
          <div>
            <RadioGroup
              label={networkGroup()?.label || 'Network Access'}
              items={networkItems()}
              testIdPrefix="network"
              name={`${menuId}-network`}
              current={currentNetwork()}
              onChange={v => props.onCodexNetworkAccessChange?.(v)}
              fieldsetClass={collaborationModeItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
            />
          </div>
        </Show>
        <Show when={sandboxItems().length > 0}>
          <div>
            <RadioGroup
              label={sandboxGroup()?.label || 'Sandbox'}
              items={sandboxItems()}
              testIdPrefix="sandbox"
              name={`${menuId}-sandbox`}
              current={currentSandbox()}
              onChange={v => props.onCodexSandboxPolicyChange?.(v)}
              fieldsetClass={collaborationModeItems().length === 0 && networkItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
            />
          </div>
        </Show>
        <div>
          <RadioGroup
            label={modeGroup()?.label || 'Approval Policy'}
            items={modeItems()}
            testIdPrefix="permission-mode"
            name={`${menuId}-mode`}
            current={currentMode()}
            onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
            fieldsetClass={collaborationModeItems().length === 0 && networkItems().length === 0 && sandboxItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
          />
        </div>
      </div>
    </div>
  )
}

/** Codex trigger label (model name, effort icon, current mode). */
function CodexTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => props.codexCollaborationMode || 'default'
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
    ? optionLabel(props.availableOptionGroups, 'codexCollaborationMode', currentCollaborationMode())
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
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isCodexNotifThread(wrapper)) {
      // Filter out Codex rate limit messages where all tiers are "allowed".
      const msgs = wrapper.messages.filter(m =>
        !isObject(m) || !isCodexRateLimitAllAllowed(m as Record<string, unknown>))
      if (msgs.length === 0)
        return { kind: 'hidden' }
      return { kind: 'notification_thread', messages: msgs }
    }

    // Empty wrapper — hide.
    if (wrapper && wrapper.messages.length === 0)
      return { kind: 'hidden' }

    if (!parent)
      return { kind: 'unknown' }

    // Startup and status notifications are transient lifecycle signals.
    // Persist them if needed, but keep them out of chat rendering.
    if (parent.method === 'thread/started' || parent.method === 'turn/started' || parent.method === 'thread/status/changed')
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
    if (parent.method === 'account/rateLimits/updated') {
      if (isCodexRateLimitAllAllowed(parent))
        return { kind: 'hidden' }
      return { kind: 'notification' }
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
      if (cat.toolName === 'plan')
        return codexPlanRenderer(cat.toolUse, role, context)
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
  async changeCollaborationMode(workerId: string, agentId: string, mode: string): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { codexCollaborationMode: mode },
    })
  },

  ControlContent: CodexControlContent,
  ControlActions: CodexControlActions,

  SettingsPanel: CodexSettingsPanel,
  settingsMenuClass: styles.settingsMenuWide,

  settingsTriggerLabel: CodexTriggerLabel,
}

registerProvider(AgentProvider.CODEX, codexPlugin)
