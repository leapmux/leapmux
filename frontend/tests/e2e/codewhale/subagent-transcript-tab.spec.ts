import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, subagentReportBubble, userBubbles } from '../helpers/ui'

/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker drives Codewhale's runtime API. Codewhale stores each thread and its tool results in its private native store.
 *
 * The agent tool returns a child ID at once. Codewhale omits child events from the parent stream. The Worker reads the child transcript and run record until the run ends.
 */
codewhaleTest.describe('Codewhale subagent registry', () => {
  codewhaleTest('a spawned child gets a registry row, a transcript tab and a report', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)

    // The child's prompt carries the marker, so the child's own turns reach
    // this script, and this rule answers them.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { text: 'PONG' },
    })
    const start = await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-codewhale', {
        description: 'Ask for one word',
        prompt: modelScript.prompt('Reply with the single word PONG.'),
      })],
    })
    // The runtime controls the number of parent turns after the spawn.
    // If the child finishes before the next parent request, that request carries its answer.
    // If the child finishes after that request, the runtime starts another parent turn to deliver the answer.
    await modelScript.fallback({ text: 'The subagent reported PONG.' })
    await sendMessage(page, modelScript.prompt('Delegate one word to a read-only subagent.'))
    await modelScript.waitForSteps(start + 1)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('ask_for_one_word')
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    await expectSectionPersists(page)

    // The child's summary reaches the parent transcript under the child's name.
    await expect(subagentReportBubble(page, /PONG/, 'ask_for_one_word')).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' }).first()).toBeVisible()

    // The child transcript opens on the child's prompt, and then holds the
    // child's own answer. `openChildTabFromRow` waits until the row links a child agent.
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG.' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: /^PONG$/ })).toBeVisible()
  })
})
