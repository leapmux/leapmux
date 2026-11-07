import type { ProviderPermissionPresets } from '../../providerSettings'
import { ANTIGRAVITY_MODE } from '~/generated/contracts/antigravity-protocol'

export const antigravityPermissionPresets = {
  bypass: {
    sets: { permissionMode: ANTIGRAVITY_MODE.Bypass },
  },
} satisfies ProviderPermissionPresets
