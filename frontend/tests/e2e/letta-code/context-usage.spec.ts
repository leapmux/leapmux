import { expectContextUsage } from '../helpers/contextUsage'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('the agent info grid follows the usage the model reports', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({
      text: ARITHMETIC_ANSWER_TEXT,
      usage,
    })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
