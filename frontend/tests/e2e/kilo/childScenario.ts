import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { childTaskAtStart } from '../helpers/runningChildProof'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble, userBubbles, waitForAgentIdle } from '../helpers/ui'

/**
 * The facts of a held Kilo child.
 * The child turn opens with its task, and a parent turn quotes the task in its spawn call, so the matcher anchors the
 * task. `kilo/workflow-grouping.spec.ts` selects each child by the title of its row.
 */
export const KILO_CHILD: NativeChildProfile = {
  childTask: childTaskAtStart,
  rowTitleHoldsDescription: true,
}

/**
 * Spawn one child that runs a shell probe, then follow its row from running to final, and its prompt and report into
 * its own tab. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseKiloSpawnTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)
  // The child's prompt carries the marker, so the turns it runs on its own
  // reach this script rather than the ambient scenario.
  await modelScript.rule({
    name: 'the child reports the shell result',
    when: { user: 'echo kilo-done' },
    respond: { text: 'The command printed kilo-done.' },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-kilo', {
        description: 'Run the shell probe',
        prompt: modelScript.prompt('Run `echo kilo-done` and report the result.'),
      })],
    },
    { text: 'The subagent reported kilo-done.' },
  )
  await sendMessage(page, modelScript.prompt('Spawn a subagent that runs the shell probe and reports the result.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await openChildTabFromRow(page, row)
  await expect(userBubbles(page).filter({ hasText: 'kilo-done' })).toBeVisible()
  await expect(subagentReportBubble(page, /kilo-done/)).toBeVisible()
}
