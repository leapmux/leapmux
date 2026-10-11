import type { ProviderPermissionPresets } from '../../providerSettings'
import { REASONIX_APPROVAL, REASONIX_CONFIG } from '~/generated/contracts/reasonix-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const reasonixPermissionPresets = {
  bypass: { sets: { [REASONIX_CONFIG.ToolApproval]: REASONIX_APPROVAL.Yolo } },
} satisfies ProviderPermissionPresets
