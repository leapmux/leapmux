import type { NativeChildProfile } from '../helpers/runningChildProof'
import { childTaskAtStart } from '../helpers/runningChildProof'

/**
 * The facts of a held Kilo child.
 * The child turn opens with its task, and a parent turn quotes the task in its spawn call, so the matcher anchors the
 * task. `kilo/workflow-grouping.spec.ts` selects each child by the title of its row.
 */
export const KILO_CHILD: NativeChildProfile = {
  childTask: childTaskAtStart,
  rowTitleHoldsDescription: true,
}
