import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'

gooseTest.describe('goose agent startup', () => {
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
