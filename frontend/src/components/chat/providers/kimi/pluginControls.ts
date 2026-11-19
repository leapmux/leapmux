import type { ProviderControlCapability } from '../capabilities'
import { withWorkspaceTrustResponse } from '../workspaceTrust'
import { kimiControlResponseSummary } from './controlResponse'
import { kimiPermissionPresets } from './permissionPresets'

/** The Kimi Code control facts that the browser states, separate from provider registration. */
export const kimiControls: ProviderControlCapability = {
  // Smart runs routine edits and commands, then asks before risky calls. Bypass runs every call and decides every request itself.
  // Kimi calls these choices Ask When Needed and Never Ask.
  permissionPresets: kimiPermissionPresets,
  // The Worker asks Kimi Code's workspace-trust question itself (providerkit.WorkspaceTrust),
  // so the saved answer to that question is LeapMux's own.
  controlResponseDisplay: withWorkspaceTrustResponse(kimiControlResponseSummary),
}
