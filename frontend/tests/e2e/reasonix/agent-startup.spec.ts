import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest.describe('reasonix agent startup', () => {
  for (const failed of [false, true]) {
    reasonixTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedReasonixWorkspace }) => {
      const executable = findBinary('reasonix')
      if (!executable)
        throw new Error('The startup test requires the real reasonix executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }, {
        launch: { binaryName: 'reasonix', executable, holdWhen: ['acp'] },
        failed,
      })
    })
  }
})
