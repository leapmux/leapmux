import { expect } from '@playwright/test'
import { exerciseModelError } from '../helpers/nativeModelError'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

opencodeTest('keeps only the retried answer after a mid-stream failure', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue(
    {
      error: { status: 503, message: 'Service unavailable', midStream: true },
    },
    { text: 'FULL-ANSWER-AFTER-RETRY' },
  )
  await sendMessage(page, modelScript.prompt('Stream the scripted answer.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: 'FULL-ANSWER-AFTER-RETRY' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'partial ' })).toHaveCount(0)
    const retry = messageContents(page).filter({ hasText: 'API retry' })
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('succeeded')
  }
})
