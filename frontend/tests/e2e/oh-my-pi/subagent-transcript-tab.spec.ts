import { expect } from '@playwright/test'
import { ohMyPiYieldToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble, userBubbles } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiChildTurn } from './childScenario'

/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Each task entry starts a separate child. Native subagent events identify its messages and progress. The yield tool ends that child. The test profile disables background tasks, so the parent waits for the child.
 */
/** What the subagent yields, and what its row and its transcript report. */
const REPORT = 'Apple, banana, cherry. One, two, three. Done.'

ohMyPiTest.describe('Oh My Pi subagent registry', () => {
  ohMyPiTest('follows a subagent from its spawn to its report', async ({ native }) => {
    const { page, modelScript } = native
    await expectNoRegistryRows(page, native.leapmuxServer)

    await modelScript.rule({
      name: 'the subagent yields its report',
      when: ohMyPiChildTurn(),
      respond: { toolCalls: [ohMyPiYieldToolCall('yield-report', REPORT)] },
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-omp', {
          description: 'Run the fruit task',
          prompt: modelScript.prompt('List three fruits, then count to three, then report done.'),
        })],
      },
      { text: 'The subagent listed three fruits and counted to three.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's choice.
    const row = await requireRegistryRow(page)
    // The row's title is omp's id for the subagent, which is the task's name.
    await expect(row).toContainText('run_the_fruit_task')
    await modelScript.waitForSteps(start + 2)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    expect((await modelScript.status()).ruleMatches['the subagent yields its report']).toBeGreaterThan(0)

    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: /list three fruits/i })).toBeVisible()
    // The report states the subagent by its id, as its row does, and carries
    // what the subagent yielded.
    await expect(subagentReportBubble(page, REPORT, 'run_the_fruit_task')).toBeVisible()
  })
})
