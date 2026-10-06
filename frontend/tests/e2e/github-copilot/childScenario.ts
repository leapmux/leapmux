import type { NativeChildProfile } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { copilotChildTaskMatcher, readCopilotChildTaskId } from './childIdentity'

/**
 * The facts of a held Copilot child.
 * The Bypass preset lets the spawn run without a permission request. The start event of the child selects it by the
 * spawn call, so the row title does not.
 */
export const COPILOT_CHILD: NativeChildProfile = {
  childTask: copilotChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
  resolveTaskId: (context, parentId, child) => readCopilotChildTaskId(context, parentId, child.spawn.id),
}
