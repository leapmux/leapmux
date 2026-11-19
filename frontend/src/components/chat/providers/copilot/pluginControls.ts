import type { ProviderControlCapability } from '../capabilities'
import { withWorkspaceTrustResponse } from '../workspaceTrust'
import { copilotControlResponseSummary } from './controlResponse'
import { copilotPermissionPresets } from './permissionPresets'

/** The Copilot control facts that the browser states, separate from provider registration. */
export const copilotControls: ProviderControlCapability = {
  permissionPresets: copilotPermissionPresets,
  // The Worker asks Copilot's folder-trust question itself (providerkit.WorkspaceTrust),
  // so the saved answer to that question is LeapMux's own.
  controlResponseDisplay: withWorkspaceTrustResponse(copilotControlResponseSummary),
}
