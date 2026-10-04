import type { ProviderConfigurationCapability } from '../capabilities'
import { DEEPSEEK_HARNESS_MODE } from '~/generated/contracts/deepseek-harness-protocol'
import { buildPlanMode } from '../../settingsGroups'

export const deepseekHarnessConfiguration: ProviderConfigurationCapability = {
  attachments: { text: true, image: true, pdf: true, binary: true },
  planMode: buildPlanMode('permissionMode', DEEPSEEK_HARNESS_MODE.Plan, DEEPSEEK_HARNESS_MODE.Act),
}
