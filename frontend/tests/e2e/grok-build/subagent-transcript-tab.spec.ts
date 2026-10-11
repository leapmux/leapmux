import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, openWorkspace, sendMessage, userBubbles } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { grokChildTurn } from './childScenario'
import { GROK_AGENT } from './scenarios'

/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker drives Grok Build through the Agent Client Protocol.
 *
 * Grok sends child output through its _x.ai/session_notification extension. Its native subagent cancel stops a selected child.
 */
grokTest.describe('Grok Build subagent registry', () => {
  grokTest('a foreground subagent opens a row and a child transcript with its report', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page, leapmuxServer)

    // The child's prompt carries the marker, so the turn it runs reaches this
    // script. A rule rather than a step: the child's turn has no order the test
    // controls against the parent's.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: grokChildTurn('Reply with the single word PONG'),
      respond: { text: 'PONG' },
    })
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.GROK_BUILD, 'spawn-grok', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps(start + 2)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect(messageBubbles(page).filter({ hasText: 'Agent "Ask for one word" completed' }).first()).toBeVisible()
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })
})
