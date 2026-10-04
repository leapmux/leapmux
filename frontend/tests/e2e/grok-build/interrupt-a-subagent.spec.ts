import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from '../grok-fixtures'
import { exerciseChildInterrupt, expectNoRegistryRows, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { openWorkspace } from '../helpers/ui'

/**
 * The child tab interrupts only its actual native child. The parent must remain operational.
 *
 * The Worker drives Grok Build through the Agent Client Protocol.
 *
 * Grok sends child output through its _x.ai/session_notification extension. Its native subagent cancel stops a selected child.
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
  grokTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectNoRegistryRows(page, leapmuxServer)
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.GROK_BUILD,
      childTurn: { system: GROK_SUBAGENT_SYSTEM, user: HELD_CHILD_TASK },
    })
  })
})
