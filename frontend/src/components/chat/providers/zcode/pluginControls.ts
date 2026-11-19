import type { ProviderControlCapability } from '../capabilities'
import { withWorkspaceTrustResponse } from '../workspaceTrust'
import { zcodeControlResponseSummary } from './controlResponse'
import { zcodePermissionPresets } from './permissionPresets'

/** The ZCode control facts that the browser states, separate from provider registration. */
export const zcodeControls: ProviderControlCapability = {
  // The Worker asks ZCode's hook-trust question itself (providerkit.WorkspaceTrust),
  // so the saved answer to that question is LeapMux's own.
  controlResponseDisplay: withWorkspaceTrustResponse(zcodeControlResponseSummary),
  permissionPresets: zcodePermissionPresets,
}
