import type { ProviderPermissionPresets } from '../../providerSettings'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '~/generated/contracts/kiro-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const kiroPermissionPresets = {
  bypass: { sets: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } },
} satisfies ProviderPermissionPresets
