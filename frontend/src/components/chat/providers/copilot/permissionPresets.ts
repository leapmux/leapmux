import type { ProviderPermissionPresets } from '../../providerSettings'
import { COPILOT_PERMISSION_MODE } from '~/generated/contracts/copilot-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const copilotPermissionPresets = {
  smart: { sets: { permissionMode: COPILOT_PERMISSION_MODE.Assisted } },
  bypass: { sets: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } },
} satisfies ProviderPermissionPresets
