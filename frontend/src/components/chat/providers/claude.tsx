import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps } from './registry'
import type { PermissionMode } from '~/utils/controlResponse'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import Sparkles from 'lucide-solid/icons/sparkles'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { isNotificationThreadWrapper, isObject } from '../messageUtils'
import { EFFORTS, modeLabel, modelLabel, MODELS, PERMISSION_MODES, RadioGroup } from '../settingsShared'
import { registerProvider } from './registry'

function generateRandomId(): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  let result = '01'
  for (let i = 0; i < 22; i++) {
    result += chars[Math.floor(Math.random() * chars.length)]
  }
  return result
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
  parentObject: Record<string, unknown> | undefined,
  wrapper: { old_seqs: number[], messages: unknown[] } | null,
): MessageCategory {
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
    if (subtype === 'init')
      return { kind: 'hidden' }
    if (subtype === 'status' && parentObject.status !== 'compacting')
      return { kind: 'hidden' }
    if (subtype === 'task_notification')
      return { kind: 'task_notification' }
    return { kind: 'notification' }
  }

  // Non-system notification types
  if (type === 'rate_limit') {
    if (isObject(parentObject.rate_limit_info) && (parentObject.rate_limit_info as Record<string, unknown>).status === 'allowed')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }
  if (type === 'interrupted' || type === 'context_cleared' || type === 'settings_changed' || type === 'agent_renamed')
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
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        if ((content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'tool_result'))
          return { kind: 'tool_result' }
      }
    }
    return { kind: 'unknown' }
  }

  // Plain object with string .content and no .type → user_content
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
}

const DEFAULT_CLAUDE_MODEL = import.meta.env.LEAPMUX_DEFAULT_CLAUDE_MODEL || 'opus'
const DEFAULT_CLAUDE_EFFORT = import.meta.env.LEAPMUX_DEFAULT_CLAUDE_EFFORT || 'high'

/** Claude Code settings panel (model, effort, permission mode). */
function ClaudeCodeSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'
  const isOpus = () => currentModel().startsWith('opus')
  const availableEfforts = () => isOpus() ? EFFORTS : EFFORTS.filter(e => e.value !== 'max')

  return (
    <>
      <Show when={props.supportsModelEffort !== false}>
        <Show when={currentModel() !== 'haiku'}>
          <RadioGroup
            label="Effort"
            items={availableEfforts()}
            testIdPrefix="effort"
            name={`${menuId}-effort`}
            current={currentEffort()}
            onChange={v => props.onEffortChange?.(v)}
          />
        </Show>
        <RadioGroup
          label="Model"
          items={MODELS}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={(v) => {
            props.onModelChange?.(v)
            if (!v.startsWith('opus') && currentEffort() === 'max') {
              props.onEffortChange?.('high')
            }
          }}
        />
      </Show>
      <RadioGroup
        label="Permission Mode"
        items={PERMISSION_MODES}
        testIdPrefix="permission-mode"
        name={`${menuId}-mode`}
        current={currentMode()}
        onChange={v => props.onPermissionModeChange?.(v as PermissionMode)}
      />
    </>
  )
}

/** Claude Code trigger label (model, effort icon, permission mode). */
function ClaudeCodeTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'auto': return <Icon icon={Sparkles} size="xs" />
      case 'low': return <Icon icon={ChevronsDown} size="xs" />
      case 'high': return <Icon icon={ChevronsUp} size="xs" />
      case 'max': return <Icon icon={Zap} size="xs" />
      default: return <Icon icon={Dot} size="xs" />
    }
  }

  return (
    <>
      <Show when={props.supportsModelEffort !== false}>
        {modelLabel(currentModel())}
        <Show when={currentModel() !== 'haiku'}>{effortIcon()}</Show>
      </Show>
      {modeLabel(currentMode())}
    </>
  )
}

const claudeCodePlugin: ProviderPlugin = {
  defaultModel: DEFAULT_CLAUDE_MODEL,
  defaultEffort: DEFAULT_CLAUDE_EFFORT,

  classify: classifyClaudeCodeMessage,

  buildInterruptContent(): string | null {
    return buildInterruptRequest()
  },

  // Claude Code control_response format is the native wire format —
  // return null to signal "send as-is".
  buildControlResponse(): Uint8Array | null {
    return null
  },

  SettingsPanel: ClaudeCodeSettingsPanel,

  settingsTriggerLabel: ClaudeCodeTriggerLabel,
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)
