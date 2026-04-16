import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ClassificationContext, ClassificationInput, ProviderPlugin, ProviderSettingsPanelProps } from './registry'
import type { PermissionMode } from '~/utils/controlResponse'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import Sparkles from 'lucide-solid/icons/sparkles'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { Icon } from '~/components/common/Icon'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getToolName } from '~/utils/controlResponse'
import * as styles from '../ChatView.css'
import { ClaudeCodeControlActions, ClaudeCodeControlContent } from '../controls/ClaudeCodeControlRequest'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { ALWAYS_THINKING_KEY, effortItems, FAST_MODE_KEY, hasEfforts, modelDisplayName, modelItems, ModelSelect, optionGroup, optionGroupDefaultValue, optionGroupItems, optionLabel, OUTPUT_STYLE_KEY, PERMISSION_MODE_KEY, permissionModeGroup, permissionModeItems, RadioGroup } from '../settingsShared'
import { renderClaudeMessage } from './claudeRenderers'
import { registerProvider } from './registry'

function generateRandomId(): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  let result = '01'
  for (let i = 0; i < 22; i++) {
    result += chars[Math.floor(Math.random() * chars.length)]
  }
  return result
}

function buildSetPermissionModeRequest(mode: PermissionMode): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
    request: { subtype: 'set_permission_mode', mode },
  })
}

function buildInterruptRequest(): string {
  const requestId = generateRandomId()
  return JSON.stringify({
    type: 'control_request',
    request_id: requestId,
    request: { subtype: 'interrupt' },
  })
}

/** Extra notification types for Claude Code (plan_execution, system subtypes). */
const CLAUDE_EXTRA_TYPES = new Set(['plan_execution'])
function isClaudeNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CLAUDE_EXTRA_TYPES, (t, st) =>
    t === 'system' && st !== 'init' && st !== 'task_notification')
}

/** Claude Code message classification. */
function classifyClaudeCodeMessage(
  input: ClassificationInput,
  _context?: ClassificationContext,
): MessageCategory {
  const parentObject = input.parentObject
  const wrapper = input.wrapper

  // 0. Empty wrapper (all notifications consolidated to no-ops) — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // 1. Notification thread (wrapper with notification-type first message)
  if (isClaudeNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter((m) => {
      if (!isObject(m))
        return true
      return !((m as Record<string, unknown>).type === 'rate_limit' && isObject((m as Record<string, unknown>).rate_limit_info)
        && ((m as Record<string, unknown>).rate_limit_info as Record<string, unknown>).status === 'allowed')
    })
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification_thread', messages: msgs }
  }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = parentObject.type as string | undefined
  const subtype = parentObject.subtype as string | undefined

  // 2. Hidden: system init, or system status (non-compacting)
  if (type === 'system') {
    if (input.parentSpanId && (subtype === 'task_started' || subtype === 'task_progress'))
      return { kind: 'hidden' }
    if (subtype === 'init')
      return { kind: 'hidden' }
    if (subtype === 'status' && parentObject.status !== 'compacting')
      return { kind: 'hidden' }
    if (subtype === 'task_notification')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }

  // Non-system notification types
  if (type === 'rate_limit') {
    if (isObject(parentObject.rate_limit_info) && (parentObject.rate_limit_info as Record<string, unknown>).status === 'allowed')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }
  if (type === 'interrupted' || type === 'context_cleared' || type === 'compacting' || type === 'settings_changed' || type === 'agent_renamed')
    return { kind: 'notification' }

  // Result divider
  if (type === 'result')
    return { kind: 'result_divider' }

  // Compact summary
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // Control response (synthetic message with controlResponse)
  if (parentObject.isSynthetic === true && isObject(parentObject.controlResponse))
    return { kind: 'control_response' }

  // Assistant messages
  if (type === 'assistant') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (Array.isArray(content)) {
        const contentArr = content as Array<Record<string, unknown>>
        const toolUse = contentArr.find(c => isObject(c) && c.type === 'tool_use') as Record<string, unknown> | undefined
        if (toolUse) {
          if (input.spanType === 'ToolSearch')
            return { kind: 'hidden' }
          return {
            kind: 'tool_use',
            toolName: String(toolUse.name || ''),
            toolUse,
            content: contentArr,
          }
        }
        if (contentArr.some(c => isObject(c) && c.type === 'text'))
          return { kind: 'assistant_text' }
        if (contentArr.some(c => isObject(c) && c.type === 'thinking'))
          return { kind: 'assistant_thinking' }
      }
    }
    return { kind: 'unknown' }
  }

  // User messages
  if (type === 'user') {
    if (input.spanType === 'EnterPlanMode' || parentObject.span_type === 'EnterPlanMode')
      return { kind: 'hidden' }

    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        // tool_result takes priority over agent_prompt (subagent tool results
        // also have parent_tool_use_id but should be rendered as tool results).
        if ((content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'tool_result')) {
          if (input.spanType === 'TodoWrite' || input.spanType === 'ToolSearch')
            return { kind: 'hidden' }
          return { kind: 'tool_result' }
        }
      }
    }
    // Agent prompt: user message with parent_tool_use_id (prompt sent to sub-agent)
    if (typeof parentObject.parent_tool_use_id === 'string')
      return { kind: 'agent_prompt' }
    return { kind: 'unknown' }
  }

  // Plain object with string .content and no .type → user_content
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    if (parentObject.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
}

const DEFAULT_CLAUDE_MODEL = import.meta.env.LEAPMUX_CLAUDE_DEFAULT_MODEL || 'opus[1m]'
const DEFAULT_CLAUDE_EFFORT = import.meta.env.LEAPMUX_CLAUDE_DEFAULT_EFFORT || 'high'

/** Claude Code settings panel (two-column: left = thinking/effort/model, right = fast mode/output style/permission mode). */
function ClaudeCodeSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'
  const currentOutputStyle = () => props.extraSettings?.[OUTPUT_STYLE_KEY] || optionGroupDefaultValue(props.availableOptionGroups, OUTPUT_STYLE_KEY) || 'default'
  const currentFastMode = () => props.extraSettings?.[FAST_MODE_KEY] || optionGroupDefaultValue(props.availableOptionGroups, FAST_MODE_KEY) || 'off'
  const currentThinking = () => props.extraSettings?.[ALWAYS_THINKING_KEY] || optionGroupDefaultValue(props.availableOptionGroups, ALWAYS_THINKING_KEY) || 'on'

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const hasEffort = () => efforts().length > 0
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)
  const outputStyleItems = () => optionGroupItems(props.availableOptionGroups, OUTPUT_STYLE_KEY)
  const fastModeItems = () => optionGroupItems(props.availableOptionGroups, FAST_MODE_KEY)
  const thinkingItems = () => optionGroupItems(props.availableOptionGroups, ALWAYS_THINKING_KEY)

  // Identify the first visible group in each column so settingsFieldsetFirst
  // is applied only to it.
  const leftFirstGroup = () => thinkingItems().length > 0 ? 'thinking' : 'effort'
  const rightFirstGroup = () => fastModeItems().length > 0 ? 'fast' : outputStyleItems().length > 0 ? 'output' : 'mode'
  const firstLeftClass = (id: string) => leftFirstGroup() === id ? styles.settingsFieldsetFirst : undefined
  const firstRightClass = (id: string) => rightFirstGroup() === id ? styles.settingsFieldsetFirst : undefined

  return (
    <div class={styles.settingsPanelColumns}>
      <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnSlightlyWider].join(' ')}>
        <Show when={thinkingItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, ALWAYS_THINKING_KEY)?.label || 'Extended Thinking'}
            items={thinkingItems()}
            testIdPrefix="thinking"
            name={`${menuId}-thinking`}
            current={currentThinking()}
            onChange={v => props.onOptionGroupChange?.(ALWAYS_THINKING_KEY, v)}
            fieldsetClass={firstLeftClass('thinking')}
          />
        </Show>
        <Show when={props.availableModels && props.availableModels.length > 0}>
          <Show when={hasEffort()}>
            <RadioGroup
              label="Effort"
              items={efforts()}
              testIdPrefix="effort"
              name={`${menuId}-effort`}
              current={currentEffort()}
              onChange={v => props.onEffortChange?.(v)}
              fieldsetClass={firstLeftClass('effort')}
            />
          </Show>
          <ModelSelect
            items={models()}
            testIdPrefix="model"
            name={`${menuId}-model`}
            current={currentModel()}
            onChange={(v) => {
              props.onModelChange?.(v)
              // If switching away from opus and effort is max, downgrade to high
              if (!v.startsWith('opus') && currentEffort() === 'max') {
                props.onEffortChange?.('high')
              }
            }}
          />
        </Show>
      </div>
      <div class={styles.settingsPanelColumn}>
        <Show when={fastModeItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, FAST_MODE_KEY)?.label || 'Fast Mode'}
            items={fastModeItems()}
            testIdPrefix="fast-mode"
            name={`${menuId}-fast-mode`}
            current={currentFastMode()}
            onChange={v => props.onOptionGroupChange?.(FAST_MODE_KEY, v)}
            fieldsetClass={firstRightClass('fast')}
          />
        </Show>
        <Show when={outputStyleItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, OUTPUT_STYLE_KEY)?.label || 'Output Style'}
            items={outputStyleItems()}
            testIdPrefix="output-style"
            name={`${menuId}-output-style`}
            current={currentOutputStyle()}
            onChange={v => props.onOptionGroupChange?.(OUTPUT_STYLE_KEY, v)}
            fieldsetClass={firstRightClass('output')}
          />
        </Show>
        <div>
          <RadioGroup
            label={modeGroup()?.label || 'Permission Mode'}
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

/** Claude Code trigger label (model, effort icon, permission mode). */
function ClaudeCodeTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'

  const displayName = () => modelDisplayName(props.availableModels, currentModel())

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'auto': return <Icon icon={Sparkles} size="xs" />
      case 'low': return <Icon icon={ChevronsDown} size="xs" />
      case 'high': return <Icon icon={ChevronsUp} size="xs" />
      case 'max': return <Icon icon={Zap} size="xs" />
      default: return <Icon icon={Dot} size="xs" />
    }
  }

  const hasEffort = () => hasEfforts(props.availableModels, currentModel())
  const mode = () => optionLabel(props.availableOptionGroups, PERMISSION_MODE_KEY, currentMode())

  return (
    <>
      <Show when={props.availableModels && props.availableModels.length > 0}>
        {displayName()}
        <Show when={hasEffort()}>{effortIcon()}</Show>
      </Show>
      {mode()}
    </>
  )
}

const claudeCodePlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CLAUDE_MODEL,
  defaultEffort: DEFAULT_CLAUDE_EFFORT,
  defaultPermissionMode: 'default',
  bypassPermissionMode: 'bypassPermissions',
  attachments: {
    text: true,
    image: true,
    pdf: true,
    binary: false,
  },
  planMode: {
    currentMode: agent => agent.permissionMode || 'default',
    planValue: 'plan',
    defaultValue: 'default',
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyClaudeCodeMessage,
  renderMessage: renderClaudeMessage,

  isAskUserQuestion(payload) {
    const tool = getToolName(payload)
    return tool === 'AskUserQuestion' || tool === 'request_user_input'
  },

  buildInterruptContent(): string | null {
    return buildInterruptRequest()
  },

  // Claude Code supports runtime permission mode changes via control_request
  // (lightweight, no agent restart needed).
  async changePermissionMode(workerId: string, agentId: string, mode: PermissionMode): Promise<void> {
    await workerRpc.sendAgentRawMessage(workerId, {
      agentId,
      content: buildSetPermissionModeRequest(mode),
    })
  },

  ControlContent: ClaudeCodeControlContent,
  ControlActions: ClaudeCodeControlActions,

  SettingsPanel: ClaudeCodeSettingsPanel,

  settingsTriggerLabel: ClaudeCodeTriggerLabel,
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)
