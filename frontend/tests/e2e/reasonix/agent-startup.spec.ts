import { AgentProvider } from '../acp-fixture-factory'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'

reasonixTest.describe('reasonix agent startup', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON ?? '')

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
