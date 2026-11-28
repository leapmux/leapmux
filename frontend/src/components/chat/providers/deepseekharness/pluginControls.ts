import type { ProviderControlCapability } from '../capabilities'
import { deepseekHarnessPermissionPresets } from './permissionPresets'

export const deepseekHarnessControls: ProviderControlCapability = {
  permissionPresets: deepseekHarnessPermissionPresets,
}
