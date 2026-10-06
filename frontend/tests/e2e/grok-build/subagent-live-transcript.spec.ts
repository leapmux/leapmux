import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { grokChildTurn } from './childScenario'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Grok Build through the Agent Client Protocol.
 *
 * Grok sends child output through its _x.ai/session_notification extension. Its native subagent cancel stops a selected child.
 */
grokTest.describe('Grok Build subagent registry', () => {
  grokTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseLiveChildTranscript({ page, modelScript, leapmuxServer, provider: AgentProvider.GROK_BUILD }, {
      childWhen: grokChildTurn('Reply with CHILD_LIVE_DONE'),
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir } },
    })
  })
})
