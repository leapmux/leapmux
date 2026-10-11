import type { ProviderPermissionPresets } from '../../providerSettings'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'

/** The canonical permission changes that this provider's presets apply. */
export const codexPermissionPresets = { bypass: CODEX_BYPASS_SETTINGS } satisfies ProviderPermissionPresets
