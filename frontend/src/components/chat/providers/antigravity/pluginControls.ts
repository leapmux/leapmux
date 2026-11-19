import type { ProviderControlCapability } from '../capabilities'
import { antigravityControlResponseSummary } from './controlResponse'
import { antigravityPermissionPresets } from './permissionPresets'

export const antigravityControls: ProviderControlCapability = {
  controlResponseDisplay: antigravityControlResponseSummary,
  permissionPresets: antigravityPermissionPresets,
}
