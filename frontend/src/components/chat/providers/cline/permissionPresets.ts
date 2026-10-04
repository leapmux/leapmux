import type { ProviderPermissionPresets } from '../../providerSettings'
import { CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const clinePermissionPresets = { bypass: { sets: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } } } satisfies ProviderPermissionPresets
