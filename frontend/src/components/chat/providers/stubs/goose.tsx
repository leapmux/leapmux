import type { ACPSettingsPanelConfig } from '../acp/settings'
import type { PermissionMode } from '~/utils/controlResponse'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACPControlActions, ACPControlContent } from '../../controls/ACPControlRequest'
import { registerACPProvider } from '../acp/registerACPProvider'

const DEFAULT_GOOSE_MODEL = import.meta.env.LEAPMUX_GOOSE_DEFAULT_MODEL || ''
const GOOSE_MODE_AUTO = 'auto' as PermissionMode

const settingsConfig: ACPSettingsPanelConfig = {
  kind: 'permissionMode',
  defaultModel: DEFAULT_GOOSE_MODEL,
  defaultMode: GOOSE_MODE_AUTO,
  fallbackLabel: 'Mode',
  testIdPrefix: 'permission-mode',
}

registerACPProvider({
  provider: AgentProvider.GOOSE,
  settingsConfig,
  ControlContent: ACPControlContent,
  ControlActions: ACPControlActions,
  bypassPermissionMode: GOOSE_MODE_AUTO,
  extraHiddenSessionUpdates: new Set(['config_option_update']),
})
