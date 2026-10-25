import type { ProviderPermissionPresets } from '../../providerSettings'
import { DIRAC_CONFIG } from '~/generated/contracts/dirac-protocol'

/** The native bypass switch leaves Plan/Act and model choices unchanged. */
export const diracPermissionPresets = {
  bypass: { sets: { [DIRAC_CONFIG.Yolo]: 'true' } },
} satisfies ProviderPermissionPresets
