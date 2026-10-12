import type { ProviderPermissionPresets } from '../../providerSettings'
import { LETTA_MODE } from '~/generated/contracts/letta-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const lettaPermissionPresets = { bypass: { sets: { permissionMode: LETTA_MODE.Unrestricted } } } satisfies ProviderPermissionPresets
