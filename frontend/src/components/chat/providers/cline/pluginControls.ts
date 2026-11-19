import type { ProviderControlCapability } from '../capabilities'
import { clineControlResponseSummary } from './controlResponse'
import { clinePermissionPresets } from './permissionPresets'

/** The complete Cline control channel, separate from provider registration. */
export const clineControls: ProviderControlCapability = {
  controlResponseDisplay: clineControlResponseSummary,
  // Auto-approve answers every call at once, which is what Bypass means. No mode of
  // Cline's asks for the risky calls alone, so Cline offers no Smart preset.
  permissionPresets: clinePermissionPresets,
}
