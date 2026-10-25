import type { ProviderPermissionPresets } from '../../providerSettings'
import { CODEBUDDY_MODE } from '~/generated/contracts/codebuddy-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const codebuddyPermissionPresets = {
  // Smart selects Auto, in which a classifier model approves or blocks each call that would prompt. CodeBuddy accepts
  // Auto during a session although get_available_modes omits it.
  smart: { sets: { permissionMode: CODEBUDDY_MODE.Auto } },
  // CodeBuddy advertises bypassPermissions and can select it during a session.
  // The plus menu and the permission banner select this mode through the preset.
  bypass: { sets: { permissionMode: CODEBUDDY_MODE.BypassPermissions } },
} satisfies ProviderPermissionPresets
