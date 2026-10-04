import type { ProviderPermissionPresets } from '../../providerSettings'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '~/generated/contracts/mimo-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const mimoPermissionPresets = { bypass: { sets: { [MIMO_OPTION.PermissionPolicy]: MIMO_PERMISSION_POLICY.Bypass } } } satisfies ProviderPermissionPresets
