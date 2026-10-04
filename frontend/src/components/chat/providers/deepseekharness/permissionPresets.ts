import type { ProviderPermissionPresets } from '../../providerSettings'
import { DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '~/generated/contracts/deepseek-harness-protocol'

/** Select the native preset that removes sandbox and approval restrictions. */
export const deepseekHarnessPermissionPresets = {
  bypass: {
    sets: { [DEEPSEEK_HARNESS_OPTION.Permissions]: DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess },
  },
} satisfies ProviderPermissionPresets
