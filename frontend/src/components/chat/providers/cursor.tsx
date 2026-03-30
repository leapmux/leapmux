import type { ACPSettingsPanelConfig } from './acpShared'
import type { PermissionMode } from '~/utils/controlResponse'
import { createMemo, Show } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../controls/ACPControlRequest'
import { CursorControlActions, CursorControlContent } from '../controls/CursorControlRequest'
import { PERMISSION_MODE_KEY } from '../settingsShared'
import {
  buildACPInterruptContent,
  changeACPPermissionMode,
  classifyACPMessage,
  createACPSettingsPanel,
  createACPTriggerLabel,
  renderACPMessage,
} from './acpShared'
import { registerProvider } from './registry'

const DEFAULT_CURSOR_MODEL = import.meta.env.LEAPMUX_CURSOR_DEFAULT_MODEL || 'auto'

const settingsConfig: ACPSettingsPanelConfig = {
  defaultModel: DEFAULT_CURSOR_MODEL,
  optionGroupKey: PERMISSION_MODE_KEY,
  defaultOptionValue: 'agent',
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerProvider(AgentProvider.CURSOR_CLI, {
  defaultModel: DEFAULT_CURSOR_MODEL,
  defaultPermissionMode: 'agent' as PermissionMode,
  attachments: { text: true, image: true, pdf: true, binary: true },
  planMode: {
    currentMode: agent => agent.permissionMode || 'agent',
    planValue: 'plan',
    defaultValue: 'agent',
    setMode: (mode, cb) => cb.onPermissionModeChange?.(mode as PermissionMode),
  },

  classify: classifyACPMessage({ extraHiddenSessionUpdates: new Set(['config_option_update']) }),
  renderMessage: renderACPMessage,
  buildInterruptContent: buildACPInterruptContent,
  changePermissionMode: changeACPPermissionMode,

  isAskUserQuestion(payload?: Record<string, unknown>): boolean {
    return payload?.method === 'cursor/ask_question'
  },

  ControlContent: (props) => {
    const isCursorControl = createMemo(() => (
      props.request.payload.method === 'cursor/ask_question'
      || props.request.payload.method === 'cursor/create_plan'
    ))
    return (
      <Show when={isCursorControl()} fallback={<ACPControlContent {...props} />}>
        <CursorControlContent {...props} />
      </Show>
    )
  },
  ControlActions: (props) => {
    const isCursorControl = createMemo(() => (
      props.request.payload.method === 'cursor/ask_question'
      || props.request.payload.method === 'cursor/create_plan'
    ))
    return (
      <Show when={isCursorControl()} fallback={<ACPControlActions {...props} />}>
        <CursorControlActions {...props} />
      </Show>
    )
  },
  SettingsPanel: createACPSettingsPanel(settingsConfig),
  settingsTriggerLabel: createACPTriggerLabel(settingsConfig),
})
