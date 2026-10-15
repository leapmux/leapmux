import type { ProviderPermissionPresets } from '../../providerSettings'
import { CODEX_PERMISSION_PRESETS } from '~/generated/contracts/codex-permission-presets'

/**
 * The canonical permission changes that this provider's presets apply. Smart selects
 * Codex's auto-review reviewer, which upstream Codex calls Approve for me. Bypass
 * selects full access. The worker reads the same contract to expand a plan approval
 * and to answer the requests that Codex still sends after a switch to Bypass.
 */
export const codexPermissionPresets = {
  smart: CODEX_PERMISSION_PRESETS.smart,
  bypass: CODEX_PERMISSION_PRESETS.bypass,
} satisfies ProviderPermissionPresets
