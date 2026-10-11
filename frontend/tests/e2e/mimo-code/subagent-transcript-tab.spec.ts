/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker drives MiMo Code's native HTTP server. MiMo identifies each child actor in its events.
 *
 * MiMo tags each child message with its actor ID. The Worker routes those messages into that child's transcript.
 */
import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'
import { mimoChildTurn } from './childScenario'

mimoTest.describe('MiMo Code subagent registry', () => {
  mimoTest('an actor run opens a registry row and a child transcript', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)

    // The child's prompt carries the marker, so its turn reaches this script.
    // `mimoChildTurn` anchors the task, so the parent's requests, which quote
    // the task in the spawn call, do not match. The child answers in the report
    // form that MiMo asks its actors for.
    const childTask = 'Reply with the single word PONG.'
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: mimoChildTurn(childTask),
      respond: { text: '**Status**: success\n**Summary**: replied\n\nPONG' },
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-mimo', {
          description: 'Ask the subagent for one word',
          prompt: modelScript.prompt(childTask),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Run one subagent and report what it says.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    // `openChildTabFromRow` waits until the row links a child agent.
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    // The child's own answer. The task above holds the word too, so only an agent
    // bubble proves that the child answered.
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()

    // MiMo reports no turn that a message starts on an idle subagent, so the worker
    // refuses the message and states why. The queue keeps it as a failed item.
    await sendMessage(page, 'Reply with the word PING too.')
    const queue = page.locator('[data-testid="agent-input-queue"]:visible')
    await expect(queue).toContainText('Failed')
    await expect(queue).toContainText('only while the subagent runs')
    await expect(userBubbles(page).filter({ hasText: 'Reply with the word PING too.' })).toHaveCount(0)
  })
})
