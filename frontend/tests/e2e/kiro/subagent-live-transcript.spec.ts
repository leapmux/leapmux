import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { kiroTest } from '../kiro-fixtures'
import { kiroChildTurn } from './childScenario'
import { nativeContext } from './scenarios'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Kiro's v3 engine through the Agent Client Protocol.
 *
 * Kiro tags each child update with its subtask ID. The spawning parent call identifies the registry row and ends with the child's report.
 */
kiroTest.describe('Kiro subagent registry', () => {
  kiroTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { workingDir } = await openNativeAgent(context, { overrides: { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } } })
    await exerciseLiveChildTranscript(context, {
      childWhen: kiroChildTurn('Reply with CHILD_LIVE_DONE'),
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir } },
    })
  })
})
