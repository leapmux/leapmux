import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { childTaskAtStart } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble, userBubbles } from '../helpers/ui'
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

/**
 * Spawn one foreground child with a task of several steps, and follow its row from running to final, and its prompt
 * and report into its own tab. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exercisePiForegroundChild(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const gate = `pi-counting-child-${uniqueMarker()}`
  const report = 'Apple, banana, cherry. One, two, three, four, five. Done.'
  await withCleanup(async () => {
    await expectNoRegistryRows(page, context.leapmuxServer)

    // The child prompt carries the scenario marker. Its anchored rule cannot
    // answer a parent request that contains the prompt in a tool argument.
    await modelScript.rule({
      name: 'the child works through its multi-step task',
      // Match the start of the actual child prompt.
      when: { user: '^List three fruits' },
      respond: { gate, text: report },
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-pi', {
          description: 'Run the fruit task',
          prompt: modelScript.prompt('List three fruits, then count to five, then report done.'),
        })],
      },
      { text: 'The subagent listed three fruits and counted to five.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))
    await modelScript.waitForGate(gate)
    await registerPiChildNoticeRule(modelScript, { name: 'the parent acknowledges the subagent notification', spawnCallId: 'spawn-pi', description: 'Run the fruit task', report, reply: 'The subagent finished the counting task.' })
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)

    // Require the actual native row. Idle waits also track active child tasks.
    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: /list three fruits/i })).toBeVisible()
    // The report bubble contains its label and the native child answer.
    await expect(subagentReportBubble(page, /Apple, banana, cherry/)).toBeVisible()
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
}
