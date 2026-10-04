import type { ProviderPermissionPresets } from '../../providerSettings'
import { CODEBUDDY_MODE } from '~/generated/contracts/codebuddy-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const codebuddyPermissionPresets = {
  // CodeBuddy advertises bypassPermissions and can select it during a session.
  // The plus menu and the permission banner select this mode through the preset. CodeBuddy offers no Smart mode.
  bypass: { sets: { permissionMode: CODEBUDDY_MODE.BypassPermissions } },
} satisfies ProviderPermissionPresets
