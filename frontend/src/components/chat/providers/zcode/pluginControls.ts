import type { ProviderControlCapability } from '../capabilities'
import { zcodePermissionPresets } from './permissionPresets'

/** The ZCode control facts that the browser states, separate from provider registration. */
export const zcodeControls: ProviderControlCapability = {
  permissionPresets: zcodePermissionPresets,
}
