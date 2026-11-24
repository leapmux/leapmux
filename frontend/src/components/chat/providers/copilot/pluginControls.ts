import type { ProviderControlCapability } from '../capabilities'
import { copilotPermissionPresets } from './permissionPresets'

/** The Copilot control facts that the browser states, separate from provider registration. */
export const copilotControls: ProviderControlCapability = {
  permissionPresets: copilotPermissionPresets,
}
