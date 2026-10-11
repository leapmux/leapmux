import type { ProviderPermissionPresets } from '../../providerSettings'
import { QWEN_MODE } from '~/generated/contracts/qwen-protocol'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'

/** The canonical permission changes that this provider's presets apply. */
export const qwenPermissionPresets = {
  smart: { sets: { [OPTION_ID_PERMISSION_MODE]: QWEN_MODE.Auto } },
  bypass: { sets: { [OPTION_ID_PERMISSION_MODE]: QWEN_MODE.Yolo } },
} satisfies ProviderPermissionPresets
