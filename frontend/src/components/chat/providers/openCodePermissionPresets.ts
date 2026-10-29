import type { ProviderPermissionPresets } from '../providerSettings'
import { OPENCODE_OPTION } from '~/generated/contracts/opencode-protocol'

/**
 * Select the Auto-approve option of the OpenCode family. The Worker then answers each permission request once, as the
 * `--auto` option of OpenCode and Kilo does in their own clients. The family has no safety-assisted preset.
 */
export const openCodePermissionPresets = {
  bypass: {
    sets: { [OPENCODE_OPTION.AutoApprove]: 'true' },
  },
} satisfies ProviderPermissionPresets
