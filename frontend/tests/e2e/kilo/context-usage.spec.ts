import { expectContextUsage } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'

kiloTest('shows the context usage that the model reports', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  const usage = { inputTokens: 12_000, outputTokens: 40 }
  await modelScript.queue({ text: 'Usage recorded.', usage })
  await sendMessage(page, modelScript.prompt('Reply once.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectContextUsage(page, usage)
})
