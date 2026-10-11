import type { ProviderPermissionPresets } from '../../providerSettings'
import { GOOSE_MODE } from '~/generated/contracts/goose-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const goosePermissionPresets = {
  smart: { sets: { permissionMode: GOOSE_MODE.SmartApprove } },
  bypass: { sets: { permissionMode: GOOSE_MODE.Auto } },
} satisfies ProviderPermissionPresets
