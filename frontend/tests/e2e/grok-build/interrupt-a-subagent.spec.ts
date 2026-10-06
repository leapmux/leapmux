import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { grokChildTurn } from './childScenario'

/**
 * The child tab interrupts only its actual native child. The parent must remain operational.
 *
 * The Worker drives Grok Build through the Agent Client Protocol.
 *
 * Grok sends child output through its _x.ai/session_notification extension. Its native subagent cancel stops a selected child.
 */
grokTest.describe('Grok Build subagent registry', () => {
  grokTest('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    page,
    authenticatedEmptyWorkspace,
    leapmuxServer,
    modelScript,
  }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseChildInterrupt({ page, modelScript, leapmuxServer, provider: AgentProvider.GROK_BUILD }, {
      childTurn: grokChildTurn(HELD_CHILD_TASK),
    })
  })
})
