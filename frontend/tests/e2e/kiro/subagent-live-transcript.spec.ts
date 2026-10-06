import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { kiroChildTurn } from './childScenario'
import { KIRO_AGENT } from './scenarios'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Kiro's v3 engine through the Agent Client Protocol.
 *
 * Kiro tags each child update with its subtask ID. The spawning parent call identifies the registry row and ends with the child's report.
 */
kiroTest.describe('Kiro subagent registry', () => {
  kiroTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseLiveChildTranscript({ page, modelScript, leapmuxServer, provider: AgentProvider.KIRO }, {
      childWhen: kiroChildTurn('Reply with CHILD_LIVE_DONE'),
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir } },
    })
  })
})
