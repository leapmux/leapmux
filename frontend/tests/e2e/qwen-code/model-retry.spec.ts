import { expect } from '@playwright/test'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'

/**
 * How many times Qwen Code 0.25 sends one model request again before it ends
 * that request with an error. Those attempts stay inside one request lifecycle,
 * so only a request that exhausts them counts as a retry round that LeapMux
 * can see; the next request lifecycle is the round that follows.
 */
const QWEN_ATTEMPTS_PER_REQUEST = 4

qwenTest.describe('Qwen Code model retries', () => {
  qwenTest('folds rounds of failures into one resolved retry row', async ({ native }) => {
    const { page, modelScript } = native
    const start = await modelScript.queue(
      ...Array.from({ length: QWEN_ATTEMPTS_PER_REQUEST }, () => ({
        error: { status: 503, message: 'probe stub: service unavailable' },
      })),
      { text: 'CEDAR_RETRY_ANSWER' },
    )
    await sendMessage(page, modelScript.prompt('Run the retry rounds probe.'))
    await modelScript.waitForSteps(start + QWEN_ATTEMPTS_PER_REQUEST + 1)
    await waitForAgentIdle(page)
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await expect(assistantBubbles(page).filter({ hasText: 'CEDAR_RETRY_ANSWER' })).toHaveCount(1)
      const retry = messageContents(page).filter({ hasText: 'API retry' })
      await expect(retry).toHaveCount(1)
      await expect(retry).toContainText('succeeded')
    }
  })
})
