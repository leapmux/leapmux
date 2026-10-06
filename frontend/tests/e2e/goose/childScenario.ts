import type { NativeChildProfile } from '../helpers/runningChildProof'
import { applyPermissionPreset } from '../helpers/ui'
import { gooseChildTaskMatcher, readGooseChildTaskId } from './childIdentity'

/**
 * The facts of a held Goose child.
 * The Bypass preset lets the delegate run without the permission judge. The task ID of the delegate frame selects the
 * child, so the row title does not.
 */
export const GOOSE_CHILD: NativeChildProfile = {
  childTask: gooseChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
  resolveTaskId: (context, parentId, child) => readGooseChildTaskId(context, parentId, child.spawn.id, child.prompt),
}
