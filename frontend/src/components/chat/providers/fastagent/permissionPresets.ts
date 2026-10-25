import type { ProviderPermissionPresets } from '../../providerSettings'
import { FASTAGENT_OPTION } from '~/generated/contracts/fastagent-protocol'

/** Select the native process flag through the setting that requires a process restart. */
export const fastAgentPermissionPresets = {
  bypass: { sets: { [FASTAGENT_OPTION.BypassPermissions]: 'true' } },
} satisfies ProviderPermissionPresets
