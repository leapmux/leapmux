import type { JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin, ProviderSettingsPanelProps } from './registry'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import {
  CODEX_MODEL_LABELS,
  CODEX_PERMISSION_MODE_LABELS,
  DEFAULT_CODEX_EFFORT,
  DEFAULT_CODEX_MODEL,
  EFFORT_LABELS,
} from '~/utils/controlResponse'
import { registerProvider } from './registry'

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** Check whether the wrapper envelope represents a notification thread. */
function isNotificationThreadWrapper(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (!wrapper || wrapper.messages.length < 1)
    return false
  const first = wrapper.messages[0] as Record<string, unknown>
  const t = first.type as string | undefined
  return t === 'settings_changed' || t === 'context_cleared' || t === 'interrupted'
    || t === 'rate_limit' || t === 'agent_renamed' || t === 'agent_error'
}

const codexPlugin: ProviderPlugin = {
  classify(parent, wrapper): MessageCategory {
    // Notification threads (settings_changed, context_cleared, etc.)
    if (isNotificationThreadWrapper(wrapper))
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
    if (parent.turn && isObj(parent.turn) && (parent.turn as Record<string, unknown>).status)
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

  SettingsPanel: ((props: ProviderSettingsPanelProps) => {
    const models = Object.entries(CODEX_MODEL_LABELS)
    const efforts = Object.entries(EFFORT_LABELS)
    const permModes = Object.entries(CODEX_PERMISSION_MODE_LABELS)

    return [
      // Model selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Model</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of models) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.model || DEFAULT_CODEX_MODEL))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onModelChange?.(select.value))
        el.appendChild(select)
        return el
      })(),
      // Effort selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Reasoning Effort</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of efforts) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.effort || DEFAULT_CODEX_EFFORT))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onEffortChange?.(select.value))
        el.appendChild(select)
        return el
      })(),
      // Permission mode selector
      (() => {
        const el = document.createElement('div')
        el.innerHTML = `<label>Approval Policy</label>`
        const select = document.createElement('select')
        select.disabled = !!props.disabled || !!props.settingsLoading
        for (const [value, label] of permModes) {
          const opt = document.createElement('option')
          opt.value = value
          opt.textContent = label
          if (value === (props.permissionMode || 'bypassPermissions'))
            opt.selected = true
          select.appendChild(opt)
        }
        select.addEventListener('change', () => props.onPermissionModeChange?.(select.value as PermissionMode))
        el.appendChild(select)
        return el
      })(),
    ] as unknown as JSX.Element
  }) as unknown as ProviderPlugin['SettingsPanel'],

  settingsTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
    const model = props.model || DEFAULT_CODEX_MODEL
    const label = CODEX_MODEL_LABELS[model] || model
    return label as unknown as JSX.Element
  },
}

registerProvider(AgentProvider.CODEX, codexPlugin)
