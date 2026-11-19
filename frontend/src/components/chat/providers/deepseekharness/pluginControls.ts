import type { ProviderControlCapability } from '../capabilities'
import { deepseekHarnessControlResponseSummary } from './controlResponse'
import { deepseekHarnessPermissionPresets } from './permissionPresets'

export const deepseekHarnessControls: ProviderControlCapability = {
  permissionPresets: deepseekHarnessPermissionPresets,
  controlResponseDisplay: deepseekHarnessControlResponseSummary,
}
