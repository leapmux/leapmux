import type { ProviderControlCapability } from '../capabilities'
import { qoderPermissionPresets } from './permissionPresets'

/**
 * The Qoder control channel.
 *
 * Auto approves calls that Qoder finds safe and asks about the rest. It is
 * the Smart preset. Bypass Permissions runs every call without a prompt, and
 * Qoder itself withdraws a pending ask when the mode changes to it. It is the
 * Bypass preset. Don't Ask denies calls instead of bypassing prompts.
 */
export const qoderControls: ProviderControlCapability = {
  permissionPresets: qoderPermissionPresets,
}
