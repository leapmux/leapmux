import type { ProviderControlCapability } from '../capabilities'
import { mimoControlResponseSummary } from './controlResponse'
import { mimoPermissionPresets } from './permissionPresets'

/** The MiMo control facts that the browser states, separate from provider registration. */
export const mimoControls: ProviderControlCapability = {
  // An MCP server's confirmation takes the shared form display, which the row
  // extraction asks first. Every other answer takes MiMo's own words.
  controlResponseDisplay: mimoControlResponseSummary,
  // MiMo has no native permission mode. LeapMux's permission policy controls two native approval switches.
  // Bypass enables both switches and approves every call, including deletes.
  permissionPresets: mimoPermissionPresets,
}
