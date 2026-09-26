import { codexTest } from './codex-fixtures'
import { expectContextUsage } from './helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex context usage', () => {
  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  codexTest('the agent info grid follows the usage the model reports', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
