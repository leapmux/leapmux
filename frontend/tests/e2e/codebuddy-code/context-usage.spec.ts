import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest } from '../codebuddy-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('the agent info grid follows the usage the model reports', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const usage = { inputTokens: 12000, outputTokens: 40 }
    await modelScript.queue({ text: 'Usage recorded.', usage })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectContextUsage(page, usage)
  })
})
