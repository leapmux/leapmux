import type { NativeChildProfile } from '../helpers/runningChildProof'
import { childTaskAnywhere } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'

/**
 * The facts of a held ZCode child.
 * ZCode keeps the task out of the last user text of a parent turn, so a matcher at any place of that text selects
 * only the child. The Bypass preset lets the spawn run without a permission request.
 */
export const ZCODE_CHILD: NativeChildProfile = {
  childTask: childTaskAnywhere,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
}
