import type { NativeChildProfile } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixChildTaskMatcher, readReasonixChildTaskId } from './childIdentity'

/**
 * The facts of a held Reasonix child.
 * The Bypass preset lets the read-only task run without a permission request. The task receipt of the parent selects
 * the child by its spawn call, so the row title does not.
 */
export const REASONIX_CHILD: NativeChildProfile = {
  childTask: reasonixChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
  resolveTaskId: (context, parentId, child) => readReasonixChildTaskId(context, parentId, child.spawn.id, child.prompt),
}
