import type { ProviderPermissionPresets } from '../../providerSettings'
import { CURSOR_OPTION } from '~/generated/contracts/cursor-protocol'

/** Select Cursor's native Run Everything launch flag through the existing restart lifecycle. */
export const cursorPermissionPresets = {
  bypass: {
    sets: { [CURSOR_OPTION.BypassPermissions]: 'true' },
  },
} satisfies ProviderPermissionPresets
