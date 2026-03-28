import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ClassificationContext, ClassificationInput, ProviderPlugin, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import * as styles from '../ChatView.css'
import { GeminiControlActions, GeminiControlContent } from '../controls/GeminiControlRequest'
import { isNotificationThreadWrapper } from '../messageUtils'
import {
  opencodeAgentMessageRenderer,
  opencodePlanRenderer,
  opencodeResultDividerRenderer,
  opencodeThoughtRenderer,
  opencodeToolCallRenderer,
  opencodeToolCallUpdateRenderer,
} from '../opencodeRenderers'
import {
  defaultModelId,
  modeLabel,
  modelDisplayName,
  modelItems,
  ModelSelect,
  permissionModeGroup,
  permissionModeItems,
  RadioGroup,
} from '../settingsShared'
import { registerProvider } from './registry'

const DEFAULT_GEMINI_MODEL = import.meta.env.LEAPMUX_GEMINI_DEFAULT_MODEL || 'auto'
const DEFAULT_GEMINI_MODE = 'default'
const GEMINI_PLAN_MODE = 'plan'

const GEMINI_EXTRA_NOTIF_TYPES = new Set(['agent_error'])

function isGeminiNotifThread(wrapper: { messages: unknown[] } | null): boolean {
  return isNotificationThreadWrapper(wrapper, GEMINI_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

function GeminiSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_GEMINI_MODEL
  const currentMode = () => props.permissionMode || DEFAULT_GEMINI_MODE
  const models = () => modelItems(props.availableModels)
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)

  return (
    <>
      <Show when={modeItems().length > 0}>
        <RadioGroup
          label={modeGroup()?.label || 'Permission Mode'}
          items={modeItems()}
          testIdPrefix="permission-mode"
          name={`${menuId}-permission-mode`}
          current={currentMode()}
          onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
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
          fieldsetClass={modeItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
        />
      </Show>
    </>
  )
}

function GeminiTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_GEMINI_MODEL
  const currentMode = () => props.permissionMode || DEFAULT_GEMINI_MODE
  return (
    <>
      {modelDisplayName(props.availableModels, currentModel())}
      {' \u00B7 '}
      {modeLabel(props.availableOptionGroups, currentMode())}
    </>
  )
}

function classifyGeminiMessage(
  input: ClassificationInput,
  _context?: ClassificationContext,
): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper

  if (wrapper) {
    if (isGeminiNotifThread(wrapper))
      return { kind: 'notification_thread', messages: wrapper.messages }
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
  }

  if (!parent)
    return { kind: 'unknown' }

  const sessionUpdate = parent.sessionUpdate as string | undefined
  const type = parent.type as string | undefined
  const subtype = parent.subtype as string | undefined

  if (sessionUpdate === 'agent_message_chunk')
    return { kind: 'assistant_text' }

  if (sessionUpdate === 'agent_thought_chunk')
    return { kind: 'assistant_thinking' }

  if (sessionUpdate === 'tool_call')
    return { kind: 'tool_use', toolName: (parent.kind as string) || 'tool_call', toolUse: parent, content: [] }

  if (sessionUpdate === 'tool_call_update') {
    const status = parent.status as string | undefined
    if (status === 'completed' || status === 'failed' || status === 'cancelled')
      return { kind: 'tool_use', toolName: (parent.kind as string) || 'tool_call_update', toolUse: parent, content: [] }
    return { kind: 'hidden' }
  }

  if (sessionUpdate === 'plan')
    return { kind: 'tool_use', toolName: 'plan', toolUse: parent, content: [] }

  if (sessionUpdate === 'usage_update' || sessionUpdate === 'available_commands_update' || sessionUpdate === 'user_message_chunk')
    return { kind: 'hidden' }

  if (parent.stopReason !== undefined)
    return { kind: 'result_divider' }

  if (type === 'system') {
    if (subtype === 'init' || subtype === 'task_notification')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }

  if (type === 'settings_changed' || type === 'context_cleared'
    || type === 'interrupted' || type === 'agent_error' || type === 'agent_renamed' || type === 'compacting') {
    return { kind: 'notification' }
  }

  if (!sessionUpdate && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    return { kind: 'user_content' }
  }

  if (!('method' in parent) && ('result' in parent || 'error' in parent) && ('id' in parent))
    return { kind: 'hidden' }

  return { kind: 'unknown' }
}

const geminiPlugin: ProviderPlugin = {
  defaultModel: DEFAULT_GEMINI_MODEL,
  defaultPermissionMode: DEFAULT_GEMINI_MODE as PermissionMode,
  attachments: {
    text: true,
    image: true,
    pdf: true,
    binary: true,
  },
  bypassPermissionMode: 'yolo',
  planMode: {
    currentMode: agent => agent.permissionMode || DEFAULT_GEMINI_MODE,
    planValue: GEMINI_PLAN_MODE,
    defaultValue: DEFAULT_GEMINI_MODE,
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyGeminiMessage,

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

  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.updateAgentSettings(workerId, {
      agentId,
      settings: { permissionMode: mode },
    })
  },

  ControlContent: GeminiControlContent,
  ControlActions: GeminiControlActions,
  SettingsPanel: GeminiSettingsPanel,
  settingsTriggerLabel: GeminiTriggerLabel,
}

registerProvider(AgentProvider.GEMINI_CLI, geminiPlugin)
