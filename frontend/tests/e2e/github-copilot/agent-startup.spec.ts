import { AgentProvider } from '../acp-fixture-factory'
import { copilotTest } from '../copilot-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'

copilotTest.describe('github-copilot agent startup', () => {
  for (const failed of [false, true]) {
    copilotTest(failed ? 'retains queued input after a real startup failure' : 'delivers queued input after the native process starts', async ({ page, modelScript, leapmuxServer, authenticatedCopilotWorkspace }) => {
      const executable = findBinary('copilot')
      if (!executable)
        throw new Error('The startup test requires the real copilot executable.')
      await exerciseAgentStartup({ page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }, {
        launch: { binaryName: 'copilot', executable, holdWhen: ['--server', '--stdio'] },
        failed,
      })
    })
  }
})
