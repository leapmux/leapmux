import type { NativeChildProfile } from '../helpers/runningChildProof'
import { childTaskAtStart } from '../helpers/runningChildProof'
import { registerPiChildNoticeRule } from './childNoticeRule'

/**
 * The facts of a held Pi child.
 * The child turn opens with its task, so the matcher anchors the task. Pi reports a completed child to its parent in a
 * notice that states the spawn call, the description, and the report, so the child registers the rule that answers
 * that notice before its answer can complete.
 */
export const PI_CHILD: NativeChildProfile = {
  childTask: childTaskAtStart,
  rowTitleHoldsDescription: false,
  beforeRelease: async (context, child) => {
    await registerPiChildNoticeRule(context.modelScript, {
      name: `the Pi notice of ${child.spawn.id}`,
      spawnCallId: child.spawn.id,
      description: child.description,
      report: child.report,
      reply: 'The native child notification arrived.',
    })
  },
}
