import type { ProviderPermissionPresets } from '../../providerSettings'
import { MUSE_APPROVAL_MODE } from '~/generated/contracts/muse-protocol'

/** The native allowAll mode runs tools without approval prompts. */
export const musePermissionPresets = {
  bypass: { sets: { permissionMode: MUSE_APPROVAL_MODE.AllowAll } },
} satisfies ProviderPermissionPresets
