import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

/**
 * 226 -- Kiro subagent registry.
 *
 * Kiro runs a subagent inside the parent's session and tags each update of it with
 * the id of its subtask, which the worker routes into the child's transcript. The
 * spawn is a tool call of the parent. The registry row takes the id of that call as
 * its key, and the call ends with the child's answer.
 */
kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro subagent registry', () => {
  kiroTest('a subagent opens a row and a child transcript with its answer', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turn it runs reaches this
    // script. A rule rather than a step: the child's turn has no order the test
    // controls against the parent's.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { body: '"agentMode":"context-gatherer"', user: 'Reply with the single word PONG' },
      respond: { text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.KIRO, 'spawn-kiro', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps()

    const row = await requireRegistryRow(page)
    await expect(row).toContainText('context-gatherer')
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await expect(messageBubbles(page).filter({ hasText: 'Agent "context-gatherer" completed' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' })).toBeVisible()
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })
})
