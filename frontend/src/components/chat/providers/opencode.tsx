import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import * as styles from '../ChatView.css'
import { OpenCodeControlActions, OpenCodeControlContent } from '../controls/OpenCodeControlRequest'
import { isNotificationThreadWrapper } from '../messageUtils'
import {
  opencodeAgentMessageRenderer,
  opencodePlanRenderer,
  opencodeResultDividerRenderer,
  opencodeThoughtRenderer,
  opencodeToolCallRenderer,
  opencodeToolCallUpdateRenderer,
} from '../opencodeRenderers'
import { defaultModelId, modelDisplayName, modelItems, ModelSelect, optionGroup, optionGroupItems, optionLabel, RadioGroup } from '../settingsShared'
import { registerProvider } from './registry'

/** Default model for OpenCode agents (discovered dynamically, fallback). */
const DEFAULT_OPENCODE_MODEL = import.meta.env.LEAPMUX_OPENCODE_DEFAULT_MODEL || ''
const DEFAULT_OPENCODE_PRIMARY_AGENT = 'build'
const OPENCODE_PLAN_PRIMARY_AGENT = 'plan'
const OPENCODE_EXTRA_PRIMARY_AGENT = 'primaryAgent'

/** Extra notification types for OpenCode (agent_error). */
const OPENCODE_EXTRA_NOTIF_TYPES = new Set(['agent_error'])

function isOpenCodeNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, OPENCODE_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

/** OpenCode settings panel (model + primary agent). */
function OpenCodeSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_OPENCODE_MODEL
  const currentPrimaryAgent = () => props.extraSettings?.[OPENCODE_EXTRA_PRIMARY_AGENT] || DEFAULT_OPENCODE_PRIMARY_AGENT
  const models = () => modelItems(props.availableModels)
  const primaryAgentGroup = () => optionGroup(props.availableOptionGroups, OPENCODE_EXTRA_PRIMARY_AGENT)
  const primaryAgentItems = () => optionGroupItems(props.availableOptionGroups, OPENCODE_EXTRA_PRIMARY_AGENT)
  const hasPrimaryAgent = () => primaryAgentItems().length > 0

  return (
    <>
      <Show when={hasPrimaryAgent()}>
        <RadioGroup
          label={primaryAgentGroup()?.label || 'Primary Agent'}
          items={primaryAgentItems()}
          testIdPrefix="primary-agent"
          name={`${menuId}-primary-agent`}
          current={currentPrimaryAgent()}
          onChange={v => props.onOptionGroupChange?.(OPENCODE_EXTRA_PRIMARY_AGENT, v)}
          fieldsetClass={styles.settingsFieldsetFirst}
        />
      </Show>
      <Show when={models().length > 0}>
        <ModelSelect
          items={models()}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={v => props.onModelChange?.(v)}
          fieldsetClass={!hasPrimaryAgent() ? styles.settingsFieldsetFirst : undefined}
        />
      </Show>
    </>
  )
}

/** OpenCode trigger label (model name + primary agent). */
function OpenCodeTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_OPENCODE_MODEL
  const currentPrimaryAgent = () => props.extraSettings?.[OPENCODE_EXTRA_PRIMARY_AGENT] || DEFAULT_OPENCODE_PRIMARY_AGENT
  const displayName = () => modelDisplayName(props.availableModels, currentModel())
  const primaryAgent = () => optionLabel(props.availableOptionGroups, OPENCODE_EXTRA_PRIMARY_AGENT, currentPrimaryAgent())
  return (
    <>
      {displayName()}
      {' \u00B7 '}
      {primaryAgent()}
    </>
  )
}

/**
 * Classify a persisted ACP message. The backend persists the `update` object
 * from sessionUpdate notifications, and the full JSON-RPC for other messages.
 */
function classifyOpenCodeMessage(
  parent: Record<string, unknown> | undefined,
  wrapper: { old_seqs: number[], messages: unknown[] } | null,
): MessageCategory {
  // Notification threads (settings_changed, context_cleared, etc.)
  if (isOpenCodeNotifThread(wrapper))
    return { kind: 'notification_thread', messages: wrapper.messages }

  // Empty wrapper — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  if (!parent)
    return { kind: 'unknown' }

  const sessionUpdate = parent.sessionUpdate as string | undefined
  const type = parent.type as string | undefined
  const subtype = parent.subtype as string | undefined

  // ACP sessionUpdate-based classification
  if (sessionUpdate === 'agent_message_chunk')
    return { kind: 'assistant_text' }

  if (sessionUpdate === 'agent_thought_chunk')
    return { kind: 'assistant_thinking' }

  if (sessionUpdate === 'tool_call')
    return { kind: 'tool_use', toolName: (parent.kind as string) || 'tool_call', toolUse: parent, content: [] }

  if (sessionUpdate === 'tool_call_update') {
    const status = parent.status as string | undefined
    if (status === 'completed' || status === 'failed')
      return { kind: 'tool_use', toolName: (parent.kind as string) || 'tool_call_update', toolUse: parent, content: [] }
    // in_progress updates are streaming — hide from chat.
    return { kind: 'hidden' }
  }

  if (sessionUpdate === 'plan')
    return { kind: 'tool_use', toolName: 'plan', toolUse: parent, content: [] }

  if (sessionUpdate === 'usage_update' || sessionUpdate === 'available_commands_update' || sessionUpdate === 'user_message_chunk')
    return { kind: 'hidden' }

  // Result messages from prompt completion
  if (parent.stopReason !== undefined)
    return { kind: 'result_divider' }

  // System messages
  if (type === 'system') {
    if (subtype === 'init')
      return { kind: 'hidden' }
    if (subtype === 'task_notification')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }

  // LeapMux notification types
  if (type === 'settings_changed' || type === 'context_cleared'
    || type === 'interrupted' || type === 'agent_error' || type === 'agent_renamed' || type === 'compacting') {
    return { kind: 'notification' }
  }

  // User content (persisted by LeapMux service layer)
  if (!sessionUpdate && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    return { kind: 'user_content' }
  }

  // JSON-RPC response (e.g. permission response echo) — hide
  if (!('method' in parent) && ('result' in parent || 'error' in parent) && ('id' in parent))
    return { kind: 'hidden' }

  return { kind: 'unknown' }
}

const opencodePlugin: ProviderPlugin = {
  defaultModel: DEFAULT_OPENCODE_MODEL || undefined,
  planMode: {
    currentMode: agent => agent.extraSettings?.[OPENCODE_EXTRA_PRIMARY_AGENT] || DEFAULT_OPENCODE_PRIMARY_AGENT,
    planValue: OPENCODE_PLAN_PRIMARY_AGENT,
    defaultValue: DEFAULT_OPENCODE_PRIMARY_AGENT,
    setMode: (mode, cb) => cb.onOptionGroupChange?.(OPENCODE_EXTRA_PRIMARY_AGENT, mode),
  },

  classify: classifyOpenCodeMessage,

  renderMessage(category: MessageCategory, parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
    if (category.kind === 'assistant_text')
      return opencodeAgentMessageRenderer(parsed)
    if (category.kind === 'assistant_thinking')
      return opencodeThoughtRenderer(parsed, _role, context)
    if (category.kind === 'result_divider')
      return opencodeResultDividerRenderer(parsed)
    if (category.kind === 'tool_use') {
      const cat = category as { toolName: string, toolUse: Record<string, unknown> }
      if (cat.toolName === 'plan')
        return opencodePlanRenderer(cat.toolUse, _role, context)
      if (cat.toolUse.sessionUpdate === 'tool_call_update')
        return opencodeToolCallUpdateRenderer(cat.toolUse, _role, context)
      return opencodeToolCallRenderer(cat.toolUse, _role, context)
    }
    return null
  },

  buildInterruptContent(agentSessionId: string): string | null {
    if (!agentSessionId)
      return null
    return JSON.stringify({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: agentSessionId },
    })
  },

  isAskUserQuestion(): boolean {
    return false
  },

  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { permissionMode: mode },
    })
  },

  ControlContent: OpenCodeControlContent,
  ControlActions: OpenCodeControlActions,
  SettingsPanel: OpenCodeSettingsPanel,
  settingsTriggerLabel: OpenCodeTriggerLabel,
}

registerProvider(AgentProvider.OPENCODE, opencodePlugin)
