import { AgentProvider } from '../acp-fixture-factory'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.describe('pi agent startup', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON ?? '')

  for (const failed of [false, true]) {
    piTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedPiWorkspace }) => {
      const executable = findBinary('pi')
      if (!executable)
        throw new Error('The startup test requires the real pi executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }, {
        launch: { binaryName: 'pi', executable, holdWhen: ['--mode', 'rpc'] },
        failed,
      })
    })
  }
})
