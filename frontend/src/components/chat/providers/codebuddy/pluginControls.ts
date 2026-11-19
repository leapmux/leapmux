import type { ProviderControlCapability } from '../capabilities'
import { codebuddyPermissionPresets } from './permissionPresets'

/** The CodeBuddy control channel. */
export const codebuddyControls: ProviderControlCapability = {
  permissionPresets: codebuddyPermissionPresets,
}
