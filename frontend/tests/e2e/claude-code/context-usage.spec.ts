import { claudeTest } from '../claude-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

claudeTest('shows native context usage and restores it after reload', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  const usage = { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 }
  await modelScript.queue({ text: 'The context usage turn ended.', usage })
  await sendMessage(page, modelScript.prompt('Complete this context usage turn.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectContextUsage(page, usage)
  await page.reload()
  await expectContextUsage(page, usage)
})
