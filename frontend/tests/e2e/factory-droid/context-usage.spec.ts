import { droidTest, expect } from '../droid-fixtures'
import { assistantBubbles, messageBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest.describe('Factory Droid context usage', () => {
  droidTest('shows the native context window after a turn and after reload', async ({ native }) => {
    const { page, modelScript } = native
    const step = await modelScript.queue({ text: 'Context measured.' })
    await sendMessage(page, modelScript.prompt('Reply once so I can inspect context usage.'))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Context measured.' }).first()).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'session_token_usage_changed' })).toHaveCount(0)

    const info = await openAgentInfoCard(page)
    const context = info.getByText('Context', { exact: true }).locator('..')
    await expect(context).toContainText('250.0k')
    await expect(context).toContainText(/[1-9]\d?%/)

    await page.reload()
    const reopened = await openAgentInfoCard(page)
    await expect(reopened.getByText('Context', { exact: true }).locator('..')).toContainText('250.0k')
    await expect(messageBubbles(page).filter({ hasText: 'session_token_usage_changed' })).toHaveCount(0)
  })
})
