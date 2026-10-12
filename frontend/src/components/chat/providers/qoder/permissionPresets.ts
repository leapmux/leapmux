import type { ProviderPermissionPresets } from '../../providerSettings'
import { QODER_MODE } from '~/generated/contracts/qoder-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const qoderPermissionPresets = { smart: { sets: { permissionMode: QODER_MODE.Auto } } } satisfies ProviderPermissionPresets
