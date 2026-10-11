import type { ProviderPermissionPresets } from '../../providerSettings'
import { KIMI_MODE } from '~/generated/contracts/kimi-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const kimiPermissionPresets = {
  smart: { sets: { permissionMode: KIMI_MODE.Yolo } },
  bypass: { sets: { permissionMode: KIMI_MODE.Auto } },
} satisfies ProviderPermissionPresets
