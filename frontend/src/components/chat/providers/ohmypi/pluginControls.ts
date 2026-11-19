import type { ProviderControlCapability } from '../capabilities'
import { ohMyPiPermissionPresets } from './permissionPresets'

/** The omp control facts that the browser states, separate from provider registration. */
export const ohMyPiControls: ProviderControlCapability = {
  permissionPresets: ohMyPiPermissionPresets,
}
