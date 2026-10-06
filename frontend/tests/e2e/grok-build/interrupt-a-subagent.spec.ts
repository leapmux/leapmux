import { grokTest } from '../grok-fixtures'
import { exerciseChildInterrupt, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { grokChildTurn } from './childScenario'
import { GROK_AGENT, nativeContext } from './scenarios'

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
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openProviderAgent(leapmuxServer, context.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, context.workspaceId)
    await exerciseChildInterrupt(context, {
      childTurn: grokChildTurn(HELD_CHILD_TASK),
    })
  })
})
