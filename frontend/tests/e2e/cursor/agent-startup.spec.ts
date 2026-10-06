import { AgentProvider } from '../acp-fixture-factory'
import { cursorTest } from '../cursor-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'

cursorTest.describe('cursor agent startup', () => {
  for (const failed of [false, true]) {
    cursorTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedCursorWorkspace }) => {
      const executable = findBinary('cursor-agent')
      if (!executable)
        throw new Error('The startup test requires the real cursor-agent executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }, {
        launch: { binaryName: 'cursor-agent', executable, holdWhen: ['acp'] },
        failed,
      })
    })
  }
})
