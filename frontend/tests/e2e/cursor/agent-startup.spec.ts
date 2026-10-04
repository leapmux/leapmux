import { AgentProvider } from '../acp-fixture-factory'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'

cursorTest.describe('cursor agent startup', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON ?? '')

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
