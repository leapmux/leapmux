import type { ProviderControlCapability } from '../capabilities'
import { droidPermissionPresets } from './permissionPresets'

/** The complete Factory Droid control channel, separate from provider registration. */
export const droidControls: ProviderControlCapability = {
  // Droid's autonomy modes are the permission-mode axis. Auto (High)
  // answers every call at once, which is what Bypass means.
  permissionPresets: droidPermissionPresets,
}
