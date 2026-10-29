import { expect } from '@playwright/test'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('folds a retried request into one resolved retry row', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    {
      error: { status: 500, message: 'probe' },
    },
    { text: 'RETRY_OK' },
  )
  await sendMessage(page, modelScript.prompt('Return the scripted answer.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'RETRY_OK' })).toHaveCount(1)
    const retry = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('succeeded')
  }
})
