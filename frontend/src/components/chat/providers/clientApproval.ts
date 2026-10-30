import type { ProviderPermissionPresets } from '../providerSettings'
import { CLIENT_APPROVAL_OPTION } from '~/generated/contracts/client-approval'

/** Approve each eligible native request once. Providers retain their deny rules. */
export const clientAutoApprovePermissionPresets = {
  bypass: {
    sets: { [CLIENT_APPROVAL_OPTION.AutoApprove]: 'true' },
  },
} satisfies ProviderPermissionPresets
