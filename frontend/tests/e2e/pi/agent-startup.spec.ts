import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest.describe('pi agent startup', () => {
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
