import type { ProviderControlCapability } from '../capabilities'
import { droidControlResponseSummary } from './controlResponse'
import { droidPermissionPresets } from './permissionPresets'

/** The complete Factory Droid control channel, separate from provider registration. */
export const droidControls: ProviderControlCapability = {
  // The saved row holds Droid's own JSON-RPC reply, not the neutral envelope.
  controlResponseDisplay: droidControlResponseSummary,
  // Droid's autonomy modes are the permission-mode axis. Auto (High)
  // answers every call at once, which is what Bypass means.
  permissionPresets: droidPermissionPresets,
}
