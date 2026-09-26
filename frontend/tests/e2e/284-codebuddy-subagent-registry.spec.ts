import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 284 — CodeBuddy Code subagent registry.
 *
 * The `Agent` tool spawns a child agent. The spawn draws a row in the
 * Background tasks section, and the row opens the child's own transcript tab.
 * The child's model call holds its task as the last user text, so a rule
 * answers it out of order from the parent's turns.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CODEBUDDY

codebuddyTest.describe('CodeBuddy Code subagent registry', () => {
  codebuddyTest('follows one subagent from its spawn to its report, with its own transcript', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await expectNoRegistryRows(page)

    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { reasoning: 'The task asks for one word.', text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-codebuddy', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))

    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Ask for one word')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    expect((await modelScript.status()).ruleMatches['the child answers its one-word task']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' })).toBeVisible()

    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })
})
