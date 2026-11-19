import type { ProviderControlCapability } from '../capabilities'
import { controlBehaviorDisplay, controlDecisionWords } from '../../persistedControlResponse'
import { qoderPermissionPresets } from './permissionPresets'

/**
 * The Qoder control channel.
 *
 * The saved row holds Qoder's own decision object, which keeps the `behavior`
 * and the `message` of the neutral envelope, so the shared behavior reader
 * supplies the display. The prompt that the Worker recorded with the answer
 * states whether the request was a plan approval, which takes Approve and
 * Reject, or a permission, which takes Allow and Deny. The shared display reads
 * a saved MCP elicitation answer before this one.
 *
 * Auto approves calls that Qoder finds safe and asks about the rest. It is
 * the Smart preset. Bypass Permissions runs every call without a prompt, and
 * Qoder itself withdraws a pending ask when the mode changes to it. It is the
 * Bypass preset. Don't Ask denies calls instead of bypassing prompts.
 */
export const qoderControls: ProviderControlCapability = {
  permissionPresets: qoderPermissionPresets,
  controlResponseDisplay: record => controlBehaviorDisplay(record.response, controlDecisionWords(record.prompt)),
}
