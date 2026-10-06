import { droidTest, expect } from '../droid-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

droidTest.describe('Factory Droid rate-limit state', () => {
  droidTest('does not invent a rate window after a native 429 retry', async ({ native }) => {
    const { page, modelScript } = native
    await expectNoRateLimitState(native, { relatedProof: async () => {
      const answer = 'The retry completed.'
      const start = await modelScript.queue(
        { error: { status: 429, code: 'rate_limit_exceeded', message: 'The model limit was reached.' } },
        nativeTextStep(native, answer),
      )
      await sendMessage(page, modelScript.prompt('Reply after the model retry.'))
      const status = await modelScript.waitForSteps(start + 2)
      // Droid retries the refused request by itself, so each of the two steps takes exactly one native request.
      expect(status.requests.filter(request => request.stepIndex === start || request.stepIndex === start + 1)).toHaveLength(2)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    } })
  })
})
