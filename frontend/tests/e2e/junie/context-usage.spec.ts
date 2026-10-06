import { expectContextUsage } from '../helpers/contextUsage'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('the agent info grid follows the usage the model reports', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({
      toolCalls: [junieAnswerToolCall('junie-usage-answer', 'Usage recorded.')],
      usage,
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
