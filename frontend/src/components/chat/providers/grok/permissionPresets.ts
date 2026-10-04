import type { ProviderPermissionPresets } from '../../providerSettings'
import { GROK_APPROVAL_MODE, GROK_OPTION } from '~/generated/contracts/grok-protocol'

/** The canonical permission changes that this provider's presets apply. */
export const grokPermissionPresets = {
  smart: { sets: { [GROK_OPTION.ApprovalMode]: GROK_APPROVAL_MODE.Auto } },
  bypass: { sets: { [GROK_OPTION.ApprovalMode]: GROK_APPROVAL_MODE.AlwaysApprove } },
} satisfies ProviderPermissionPresets
