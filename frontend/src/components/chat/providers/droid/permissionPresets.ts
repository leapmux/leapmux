import type { ProviderPermissionPresets } from '../../providerSettings'
import { DROID_MODE } from '~/generated/contracts/droid-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const droidPermissionPresets = { bypass: { sets: { permissionMode: DROID_MODE.AutoHigh } } } satisfies ProviderPermissionPresets
