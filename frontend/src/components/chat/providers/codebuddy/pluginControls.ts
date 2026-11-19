import type { ProviderControlCapability } from '../capabilities'
import { codebuddyControlResponseSummary } from './controlResponse'
import { codebuddyPermissionPresets } from './permissionPresets'

/**
 * The CodeBuddy control channel.
 *
 * The saved row holds the answer that the Worker sent to CodeBuddy, in
 * CodeBuddy's own `{allowed, reason}` shape, so the display reads CodeBuddy's
 * own fields back.
 */
export const codebuddyControls: ProviderControlCapability = {
  permissionPresets: codebuddyPermissionPresets,
  controlResponseDisplay: codebuddyControlResponseSummary,
}
