import { AgentProvider } from '../acp-fixture-factory'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest.describe('opencode agent startup', () => {
  for (const failed of [false, true]) {
    opencodeTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedOpencodeWorkspace }) => {
      const executable = findBinary('opencode')
      if (!executable)
        throw new Error('The startup test requires the real opencode executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, {
        launch: { binaryName: 'opencode', executable, holdWhen: ['acp'] },
        failed,
      })
    })
  }
})
