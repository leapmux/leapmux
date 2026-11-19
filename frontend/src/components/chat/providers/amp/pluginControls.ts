import type { ProviderControlCapability } from '../capabilities'
import { ampPermissionPresets } from './permissionPresets'

/** The complete Amp control channel, separate from provider registration. */
export const ampControls: ProviderControlCapability = {
  // Amp has two modes. Allow All answers every call at once, which is what Bypass means.
  // No mode asks for the risky calls alone, so Amp offers no Smart preset.
  permissionPresets: ampPermissionPresets,
}
