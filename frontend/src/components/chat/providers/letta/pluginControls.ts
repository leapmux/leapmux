import type { ProviderControlCapability } from '../capabilities'
import { lettaPermissionPresets } from './permissionPresets'

/** The complete Letta Code control channel, separate from provider registration. */
export const lettaControls: ProviderControlCapability = {
  // Unrestricted auto-approves every tool, which is what Bypass means.
  permissionPresets: lettaPermissionPresets,
}
