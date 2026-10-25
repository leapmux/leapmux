import type { ProviderPermissionPresets } from '../../providerSettings'
import { DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '~/generated/contracts/deepseek-harness-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const deepseekHarnessPermissionPresets = {
  // Smart selects Auto review, which the Worker's launch overlay loads: the session's model reviews each tool call,
  // and a denied call asks the user. The Worker lists Auto only while the native catalog offers it.
  smart: {
    sets: { [DEEPSEEK_HARNESS_OPTION.Permissions]: DEEPSEEK_HARNESS_PERMISSION_PRESET.Auto },
  },
  // Full access removes the sandbox and the approval questions.
  bypass: {
    sets: { [DEEPSEEK_HARNESS_OPTION.Permissions]: DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess },
  },
} satisfies ProviderPermissionPresets
