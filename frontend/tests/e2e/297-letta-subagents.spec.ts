import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.LETTA

/**
 * The task the child performs. A rule on the child's own user turn answers the
 * child alone, because the parent's requests never carry these words as their
 * last user turn.
 */
const CHILD_TASK = 'Count the files and report the number.'

lettaTest.describe('Letta Code subagents', () => {
  // Letta's `Agent` tool spawns a subagent and opens a registry row. The row is
  // clickable and opens the child's transcript in its own tab. The child runs
  // its own turn, which the rule answers off-order.
  lettaTest('routes the prompt and report into a child tab opened from the registry row', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.rule({
      name: 'the child reports its count',
      when: { user: CHILD_TASK },
      respond: { text: 'LETTA_CHILD_DONE' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-letta', {
          description: 'Count the files',
          prompt: CHILD_TASK,
        })],
      },
      { text: 'LETTA_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count to a subagent, then report.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect(row).toContainText('Count the files')
    await openChildTabFromRow(page, row)

    // The child tab draws the prompt the agent gave the child and the child's
    // own report.
    await expect(userBubbles(page).filter({ hasText: 'Count the files' })).not.toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_DONE' })).not.toHaveCount(0)
  })
})
