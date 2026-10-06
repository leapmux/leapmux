import { rateLimitWindowLabel } from '../helpers/rateLimit'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI compaction and rate limits', () => {
  qoderTest('does not show a rate-limit window from BYOK model headers', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    const rateLimits = {
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: Math.floor(Date.now() / 1000) + 3600,
    }
    await modelScript.queue({ text: 'The model response arrived near the limit.', rateLimits })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The model response arrived near the limit.' }).first()).toBeVisible()

    const card = await openAgentInfoCard(page)
    await expect(card).not.toContainText(rateLimitWindowLabel(rateLimits.type))
  })
})
