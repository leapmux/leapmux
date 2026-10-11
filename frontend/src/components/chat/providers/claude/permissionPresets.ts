import type { ProviderPermissionPresets } from '../../providerSettings'
import { CLAUDE_MODE } from '~/generated/contracts/claude-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const claudePermissionPresets = {
  smart: { sets: { permissionMode: CLAUDE_MODE.Auto } },
  bypass: { sets: { permissionMode: CLAUDE_MODE.BypassPermissions } },
} satisfies ProviderPermissionPresets
