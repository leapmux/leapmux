import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openWorkspace } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Kiro's v3 engine through the Agent Client Protocol.
 *
 * Kiro tags each child update with its subtask ID. The spawning parent call identifies the registry row and ends with the child's report.
 */
kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro subagent registry', () => {
  kiroTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.KIRO,
      childWhen: { body: '"agentMode":"context-gatherer"', user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { workingDir },
    })
  })
})
