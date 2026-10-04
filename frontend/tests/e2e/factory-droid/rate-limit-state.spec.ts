import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { rateLimitWindowLabel } from '../helpers/rateLimit'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest.describe('Factory Droid rate-limit state', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('does not invent a rate window after a native 429 retry', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { error: { status: 429, code: 'rate_limit_exceeded', message: 'The model limit was reached.' } },
      { text: 'The retry completed.' },
    )
    await sendMessage(page, modelScript.prompt('Reply after the model retry.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The retry completed.' }).first()).toBeVisible()

    const info = await openAgentInfoCard(page)
    await expect(info).not.toContainText(rateLimitWindowLabel('five_hour'))
  })
})
