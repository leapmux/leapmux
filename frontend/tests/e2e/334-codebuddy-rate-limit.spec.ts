import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from './helpers/ui'

codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code rate-limit state', () => {
  codebuddyTest('shows no rate-limit window from model response headers', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({
      text: 'The turn finished near the model limit.',
      rateLimits: {
        type: 'five_hour',
        status: 'allowed_warning',
        utilization: 0.92,
        resetsAt: Math.floor(Date.now() / 1000) + 3600,
      },
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The turn finished near the model limit.' })).toBeVisible()

    const card = await openAgentInfoCard(page)
    await expect(card).toBeVisible()
    await expect(card.getByText('5-Hour Rate Limit')).toHaveCount(0)
  })
})
