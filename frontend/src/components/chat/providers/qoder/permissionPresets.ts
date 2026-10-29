import type { ProviderPermissionPresets } from '../../providerSettings'
import { QODER_MODE } from '~/generated/contracts/qoder-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const qoderPermissionPresets = {
  smart: {
    sets: { permissionMode: QODER_MODE.Auto },
  },
  bypass: {
    sets: { permissionMode: QODER_MODE.BypassPermissions },
  },
} satisfies ProviderPermissionPresets
