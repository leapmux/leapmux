import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  exerciseChildInterrupt,
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  HELD_CHILD_TASK,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles } from './helpers/ui'

/**
 * 147 -- Grok Build subagent registry.
 *
 * Grok runs a subagent in a child session of its own and reports it through its
 * `_x.ai/session_notification` extension, which the worker routes into the
 * child's transcript. A spawn asks for approval in Grok's `ask` mode, which is
 * not this test's subject, so the agent runs with Always Approve.
 *
 * The tab of a working subagent offers Interrupt, which stops that subagent
 * through Grok's own subagent cancel.
 */
grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

/**
 * The words that open the system prompt of a Grok subagent's own turn.
 *
 * A child session asks for a session title too, and that request carries the
 * child's prompt as well. Only the child's own turn states these words, so a
 * rule that requires them leaves the title to the housekeeping rule.
 */
const GROK_SUBAGENT_SYSTEM = 'You are a Grok Build subagent\\b'

grokTest.describe('Grok Build subagent registry', () => {
  grokTest('a foreground subagent opens a row and a child transcript with its report', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turn it runs reaches this
    // script. A rule rather than a step: the child's turn has no order the test
    // controls against the parent's.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { system: GROK_SUBAGENT_SYSTEM, user: 'Reply with the single word PONG' },
      respond: { text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.GROK_BUILD, 'spawn-grok', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps()

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await expect(messageBubbles(page).filter({ hasText: 'Agent "Ask for one word" completed' }).first()).toBeVisible()
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })

  grokTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.GROK_BUILD,
      childTurn: { system: GROK_SUBAGENT_SYSTEM, user: HELD_CHILD_TASK },
    })
  })
})
