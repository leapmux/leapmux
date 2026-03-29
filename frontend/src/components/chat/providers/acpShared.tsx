import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ClassificationContext, ClassificationInput, ProviderSettingsPanelProps, RenderContext } from './registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import * as styles from '../ChatView.css'
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
  modelDisplayName,
  modelItems,
  ModelSelect,
  optionGroup,
  optionGroupItems,
  optionLabel,
  permissionModeGroup,
  permissionModeItems,
  RadioGroup,
} from '../settingsShared'

// --- Shared ACP message rendering ---

export function renderACPMessage(category: MessageCategory, parsed: unknown, role: MessageRole, context?: RenderContext): JSX.Element | null {
  if (category.kind === 'assistant_text')
    return opencodeAgentMessageRenderer(parsed)
  if (category.kind === 'assistant_thinking')
    return opencodeThoughtRenderer(parsed, role, context)
  if (category.kind === 'result_divider')
    return opencodeResultDividerRenderer(parsed)
  if (category.kind === 'tool_use') {
    const cat = category as { toolName: string, toolUse: Record<string, unknown> }
    if (cat.toolName === 'plan')
      return opencodePlanRenderer(cat.toolUse, role, context)
    if (cat.toolUse.sessionUpdate === 'tool_call_update')
      return opencodeToolCallUpdateRenderer(cat.toolUse, role, context)
    return opencodeToolCallRenderer(cat.toolUse, role, context)
  }
  return null
}

// --- Shared ACP interrupt content ---

export function buildACPInterruptContent(agentSessionId: string): string | null {
  if (!agentSessionId)
    return null
  return JSON.stringify({
    jsonrpc: '2.0',
    method: 'session/cancel',
    params: { sessionId: agentSessionId },
  })
}

// --- Shared ACP permission mode change ---

export async function changeACPPermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
  await workerRpc.updateAgentSettings(workerId, {
    agentId,
    settings: { permissionMode: mode },
  })
}

// --- Shared ACP notification thread checker ---

const ACP_EXTRA_NOTIF_TYPES = new Set(['agent_error'])

export function isACPNotifThread(wrapper: { messages: unknown[] } | null): boolean {
  return isNotificationThreadWrapper(wrapper, ACP_EXTRA_NOTIF_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

// --- Shared ACP classify factory ---

export interface ACPClassifyConfig {
  extraHiddenSessionUpdates?: Set<string>
}

export function classifyACPMessage(config: ACPClassifyConfig = {}): (input: ClassificationInput, context?: ClassificationContext) => MessageCategory {
  const baseHidden = new Set(['usage_update', 'available_commands_update', 'user_message_chunk'])
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
}

// --- Shared ACP settings panel factory ---

export interface ACPSettingsPanelConfig {
  defaultModel: string
  /** Option group key — 'permissionMode' for Copilot/Gemini, or a custom key like 'primaryAgent'. */
  optionGroupKey: string
  defaultOptionValue: string
  fallbackLabel: string
  testIdPrefix: string
}

/** Read the current option value from props based on the option group key. */
function resolveCurrentOption(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig): string {
  if (config.optionGroupKey === 'permissionMode')
    return props.permissionMode || config.defaultOptionValue
  return props.extraSettings?.[config.optionGroupKey] || config.defaultOptionValue
}

/** Dispatch an option change via the appropriate callback. */
function dispatchOptionChange(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig, value: string): void {
  if (config.optionGroupKey === 'permissionMode')
    props.onPermissionModeChange?.(value as PermissionMode)
  else
    props.onOptionGroupChange?.(config.optionGroupKey, value)
}

export function createACPSettingsPanel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  const isPermissionMode = config.optionGroupKey === 'permissionMode'
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const menuId = createUniqueId()
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => resolveCurrentOption(props, config)
    const models = () => modelItems(props.availableModels)

    const optGroup = () => isPermissionMode
      ? permissionModeGroup(props.availableOptionGroups)
      : optionGroup(props.availableOptionGroups, config.optionGroupKey)
    const optItems = () => isPermissionMode
      ? permissionModeItems(props.availableOptionGroups)
      : optionGroupItems(props.availableOptionGroups, config.optionGroupKey)

    return (
      <>
        <Show when={optItems().length > 0}>
          <RadioGroup
            label={optGroup()?.label || config.fallbackLabel}
            items={optItems()}
            testIdPrefix={config.testIdPrefix}
            name={`${menuId}-${config.testIdPrefix}`}
            current={currentOption()}
            onChange={v => dispatchOptionChange(props, config, v)}
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
            fieldsetClass={optItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
          />
        </Show>
      </>
    )
  }
}

export function createACPTriggerLabel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => resolveCurrentOption(props, config)
    return (
      <>
        {modelDisplayName(props.availableModels, currentModel())}
        {' \u00B7 '}
        {optionLabel(props.availableOptionGroups, config.optionGroupKey, currentOption())}
      </>
    )
  }
}
