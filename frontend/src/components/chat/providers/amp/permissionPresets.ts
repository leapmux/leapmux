import type { ProviderPermissionPresets } from '../../providerSettings'
import { AMP_PERMISSION_MODE } from '~/generated/contracts/amp-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const ampPermissionPresets = { bypass: { sets: { permissionMode: AMP_PERMISSION_MODE.AllowAll } } } satisfies ProviderPermissionPresets
