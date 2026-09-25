import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
/**
 * 219 — MiMo Code subagent registry.
 *
 * A MiMo subagent is an ACTOR inside the parent's own session. Its messages
 * carry the actor's id, and the worker routes them into a child transcript of
 * their own.
 */
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code subagent registry', () => {
  mimoTest('an actor run opens a registry row and a child transcript', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so its turn reaches this script. The
    // rule is ANCHORED: the parent's own requests carry the spawn call, which
    // quotes this prompt, and an unanchored rule would answer the parent too.
    // The child answers in the report form that MiMo asks its actors for.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: '^Reply with the single word' },
      respond: { text: '**Status**: success\n**Summary**: replied\n\nPONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.MIMO_CODE, 'spawn-mimo', {
          description: 'Ask the subagent for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Run one subagent and report what it says.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    // `getAttribute` answers null for an absent attribute, and null is not '', so the
    // poll reads an absent attribute as the empty id it states.
    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
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
