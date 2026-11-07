import type { ProviderConfigurationCapability } from '../capabilities'
import { ANTIGRAVITY_MODE } from '~/generated/contracts/antigravity-protocol'

export const antigravityConfiguration: ProviderConfigurationCapability = {
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: true,
    binary: true,
  },
  triggerModeGroupKey: 'permissionMode',
  planMode: {
    groupKey: 'permissionMode',
    currentMode: agent => agent.optionValues?.permissionMode ?? ANTIGRAVITY_MODE.Default,
    planValue: ANTIGRAVITY_MODE.Plan,
    defaultValue: ANTIGRAVITY_MODE.Default,
  },
  supportsSubagentSend: true,
}
