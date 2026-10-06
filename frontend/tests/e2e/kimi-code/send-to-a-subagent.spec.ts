import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('sends to a Kimi Code subagent', () => {
  kimiTest.beforeEach(async ({ native }) => {
    await prepareKimiChildRun(native.page)
  })

  // Kimi Code runs a subagent as a task of its session. The child tab's
  // composer accepts a message that becomes the child's next turn, which is
  // what the matrix calls "Send to a subagent".
  kimiTest('a message sent from the subagent tab becomes the child\'s next turn', async ({ native }) => {
    const { page, modelScript } = native
    await modelScript.rule(
      {
        name: 'the child answers its first prompt',
        when: kimiChildTurn('KIMI_CHILD_FIRST'),
        respond: { text: 'KIMI_CHILD_FIRST' },
        once: true,
      },
      {
        name: 'the child answers the follow-up',
        when: kimiChildTurn('Now reply with exactly'),
        respond: { text: 'KIMI_CHILD_FOLLOWUP' },
      },
    )
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-kimi', {
          description: 'Run the first probe',
          prompt: modelScript.prompt('Reply with exactly KIMI_CHILD_FIRST.'),
        })],
      },
      { text: 'KIMI_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent to run the first probe, then report what it said.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_FIRST' })).not.toHaveCount(0)

    // The child tab is active. Its composer sends the child's next turn. A rule answers that turn, and no queued step
    // does, so the test waits for the rule.
    await sendMessage(page, modelScript.prompt('Now reply with exactly KIMI_CHILD_FOLLOWUP.'))
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the child answers the follow-up'] ?? 0).toBeGreaterThan(0)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_FOLLOWUP' })).not.toHaveCount(0)
  })
})
