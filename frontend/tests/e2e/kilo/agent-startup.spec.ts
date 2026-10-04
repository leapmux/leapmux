import { AgentProvider } from '../acp-fixture-factory'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'

kiloTest.describe('kilo agent startup', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON ?? '')

  for (const failed of [false, true]) {
    kiloTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedKiloWorkspace }) => {
      const executable = findBinary('kilo')
      if (!executable)
        throw new Error('The startup test requires the real kilo executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, {
        launch: { binaryName: 'kilo', executable, holdWhen: ['acp'] },
        failed,
      })
    })
  }
})
