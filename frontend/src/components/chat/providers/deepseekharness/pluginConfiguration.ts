import type { ProviderConfigurationCapability } from '../capabilities'
import { DEEPSEEK_HARNESS_MODE } from '~/generated/contracts/deepseek-harness-protocol'
import { buildPlanMode, OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'

export const deepseekHarnessConfiguration: ProviderConfigurationCapability = {
  // The Act and Plan axis is the mode of this provider. The permission preset (`permissions`) is
  // a second axis that the settings menu offers and the status bar does not draw.
  triggerModeGroupKey: OPTION_ID_PERMISSION_MODE,
  attachments: { text: true, image: true, pdf: true, binary: true },
  planMode: buildPlanMode(OPTION_ID_PERMISSION_MODE, DEEPSEEK_HARNESS_MODE.Plan, DEEPSEEK_HARNESS_MODE.Act),
}
