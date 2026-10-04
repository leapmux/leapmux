import type { ProviderPermissionPresets } from '../../providerSettings'
import { CODEWHALE_POSTURE } from '~/generated/contracts/codewhale-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const codewhalePermissionPresets = {
  smart: { sets: { permissionMode: CODEWHALE_POSTURE.AutoReview } },
  bypass: { sets: { permissionMode: CODEWHALE_POSTURE.FullAccess } },
} satisfies ProviderPermissionPresets
