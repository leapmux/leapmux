import type { ProviderPermissionPresets } from '../../providerSettings'
import { OH_MY_PI_APPROVAL_MODE } from '~/generated/contracts/ohmypi-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const ohMyPiPermissionPresets = {
  // omp's `yolo` runs every tool without asking. It is the only mode that matches Bypass.
  // omp cannot decide which calls need approval, so it offers no Smart preset.
  bypass: { sets: { permissionMode: OH_MY_PI_APPROVAL_MODE.Yolo } },
} satisfies ProviderPermissionPresets
