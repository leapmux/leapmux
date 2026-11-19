import type { ProviderControlCapability } from '../capabilities'
import { mimoPermissionPresets } from './permissionPresets'

/** The MiMo control facts that the browser states, separate from provider registration. */
export const mimoControls: ProviderControlCapability = {
  // MiMo has no native permission mode. LeapMux's permission policy controls two native approval switches.
  // Bypass enables both switches and approves every call, including deletes.
  permissionPresets: mimoPermissionPresets,
}
