import type { ProviderControlCapability } from '../capabilities'
import { lettaControlResponseSummary } from './controlResponse'
import { lettaPermissionPresets } from './permissionPresets'

/** The complete Letta Code control channel, separate from provider registration. */
export const lettaControls: ProviderControlCapability = {
  // The stored row holds what the Worker sent to Letta Code: an approval_response
  // for a permission, and the question response for a question. Neither is the
  // neutral envelope that the Worker built from the decision.
  controlResponseDisplay: lettaControlResponseSummary,
  // Unrestricted auto-approves every tool, which is what Bypass means.
  permissionPresets: lettaPermissionPresets,
}
