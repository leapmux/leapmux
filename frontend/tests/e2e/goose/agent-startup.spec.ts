import { AgentProvider } from '../acp-fixture-factory'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'

gooseTest.describe('goose agent startup', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON ?? '')

  for (const failed of [false, true]) {
    gooseTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedGooseWorkspace }) => {
      const executable = findBinary('goose')
      if (!executable)
        throw new Error('The startup test requires the real goose executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }, {
        launch: { binaryName: 'goose', executable, holdWhen: ['acp'] },
        failed,
      })
    })
  }
})
