import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { childTaskAnywhere } from '../helpers/runningChildProof'
import { expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { applyPermissionPreset, subagentReportBubble, userBubbles, waitForAgentIdle } from '../helpers/ui'

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

/**
 * Spawn one child that runs a shell probe before it answers, and follow its prompt, its tool row, and its report into
 * its own tab. The tool row of the child shows while the child still runs. The transcript tab, live transcript, and
 * background task cells all run this scenario.
 */
export async function exerciseZCodeChildToolTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page } = context
  const child = await exerciseLiveChildTranscript(context, {
    // The child turns match on the last user text, not on the body: the second root turn carries the spawn call,
    // whose arguments quote the child prompt, so a body matcher would answer the root with the child line.
    childWhen: { user: 'printf zcode-tool-ok' },
    childTask: 'Use Bash to run printf zcode-tool-ok, then reply with exactly ZCODE_CHILD_PONG.',
    parentTask: 'Spawn one subagent to run the shell probe, then report what it said.',
    toolProof: { shell: { command: 'printf zcode-tool-ok' } },
    childResponse: { text: 'ZCODE_CHILD_PONG' },
    // A ZCode child sends no text of its own to the child tab. Its report closes the transcript.
    finalAnswerInChildTab: false,
  })
  await waitForAgentIdle(page)
  await expectRowBecomesFinal(page, child.row)
  await expect(userBubbles(page).filter({ hasText: 'ZCODE_CHILD_PONG' })).toBeVisible()
  await expect(subagentReportBubble(page, /ZCODE_CHILD_PONG/)).toBeVisible()
}
