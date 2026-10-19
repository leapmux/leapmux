import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

claudeTest('folds a retried request into one resolved retry row', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    { error: { status: 529, message: 'overloaded' } },
    { text: 'RETRIED_OK' },
  )
  await sendMessage(page, modelScript.prompt('Reply after the request succeeds.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    const retries = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retries).toHaveCount(1)
    await expect(retries).toContainText('succeeded')
    await expect(assistantBubbles(page).filter({ hasText: 'RETRIED_OK' })).toHaveCount(1)
  }
})
