import type { ProviderConfigurationCapability } from '../capabilities'
import { COMMAND_CODE_PERMISSION_MODE } from '~/generated/contracts/commandcode-protocol'
import { buildPlanMode } from '../../settingsGroups'

export const commandCodeConfiguration: ProviderConfigurationCapability = {
  triggerModeGroupKey: 'permissionMode',
  attachments: { text: true, image: true, pdf: false, binary: false },
  planMode: buildPlanMode('permissionMode', COMMAND_CODE_PERMISSION_MODE.Plan, COMMAND_CODE_PERMISSION_MODE.Default),
}
