import type { ACPSettingsPanelConfig } from '../acp/settings'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { registerACPProvider } from '../acp/registerACPProvider'

const DEFAULT_GEMINI_MODEL = import.meta.env.LEAPMUX_GEMINI_DEFAULT_MODEL || 'auto'
const DEFAULT_GEMINI_MODE = 'default'
const GEMINI_PLAN_MODE = 'plan'

const settingsConfig: ACPSettingsPanelConfig = {
  kind: 'permissionMode',
  defaultModel: DEFAULT_GEMINI_MODEL,
  defaultMode: DEFAULT_GEMINI_MODE as PermissionMode,
  fallbackLabel: 'Permission Mode',
  testIdPrefix: 'permission-mode',
}

registerACPProvider({
  provider: AgentProvider.GEMINI_CLI,
  settingsConfig,
  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  planValue: GEMINI_PLAN_MODE,
  bypassPermissionMode: 'yolo' as PermissionMode,
})
