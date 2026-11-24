import type { ProviderControlCapability } from '../capabilities'
import { claudePermissionPresets } from './permissionPresets'

/** The Claude control facts that the browser states, separate from provider registration. */
export const claudeControls: ProviderControlCapability = {
  permissionPresets: claudePermissionPresets,
}
