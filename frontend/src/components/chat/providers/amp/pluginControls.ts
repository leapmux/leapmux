import type { ProviderControlCapability } from '../capabilities'
import { controlBehaviorDisplay } from '../../persistedControlResponse'
import { ampPermissionPresets } from './permissionPresets'

/** The complete Amp control channel, separate from provider registration. */
export const ampControls: ProviderControlCapability = {
  // The worker answers the helper from the neutral envelope. The saved row contains that envelope.
  // The neutral reader therefore supplies the complete display.
  controlResponseDisplay: cr => controlBehaviorDisplay(cr.response),
  // Amp has two modes. Allow All answers every call at once, which is what Bypass means.
  // No mode asks for the risky calls alone, so Amp offers no Smart preset.
  permissionPresets: ampPermissionPresets,
}
