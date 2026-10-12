import type { ProviderConfigurationCapability } from '../capabilities'
import { MUSE_STARTUP_OPTION_GROUPS } from '~/generated/contracts/muse-protocol'

export const museConfiguration: ProviderConfigurationCapability = {
  startupOptionGroups: Object.values(MUSE_STARTUP_OPTION_GROUPS),
  attachments: { text: true, image: true, pdf: false, binary: false },
  triggerModeGroupKey: 'permissionMode',
  supportsSubagentSend: true,
}
