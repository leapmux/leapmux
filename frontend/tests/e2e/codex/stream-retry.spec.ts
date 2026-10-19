import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest('a retried stream keeps only the retried answer', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    { error: { status: 500, message: 'cut', midStream: true } },
    { text: 'FULL-ANSWER' },
  )
  await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'FULL-ANSWER' })).toHaveCount(1)
    // The mock sends this exact delta before its native error frame.
    await expect(messageContents(page).filter({ hasText: 'partial ' })).toHaveCount(0)
    const retry = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('succeeded')
  }
})
