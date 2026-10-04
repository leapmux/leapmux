import type { ProviderPermissionPresets } from '../../providerSettings'
import { ZCODE_MODE } from '~/generated/contracts/zcode-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const zcodePermissionPresets = { bypass: { sets: { permissionMode: ZCODE_MODE.Yolo } } } satisfies ProviderPermissionPresets
