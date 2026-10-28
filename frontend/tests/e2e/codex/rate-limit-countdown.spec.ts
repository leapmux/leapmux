import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseRateLimitWindow, nearLimitRateLimits } from '../helpers/rateLimit'
import { openAgentInfoCard } from '../helpers/ui'

codexTest('refreshes an open rate-limit countdown and removes it at expiry', async ({ native }) => {
  // Install before reload so the card's interval belongs to the page clock.
  await native.page.clock.install()
  await native.page.reload()
  const rateLimits = nearLimitRateLimits()
  await exerciseRateLimitWindow(native, rateLimits)
  const card = await openAgentInfoCard(native.page)
  const resetsAt = rateLimits.resetsAt
  if (typeof resetsAt !== 'number')
    throw new Error('The countdown scenario requires an exact reset time.')

  // Move to three minutes before the native reset. Each interval tick then
  // updates the same open card without another provider message or reload.
  await native.page.clock.setSystemTime(new Date((resetsAt - 180) * 1000))
  await native.page.clock.fastForward(60_000)
  await expect(card).toContainText('resets in 0:02')
  await native.page.clock.fastForward(60_000)
  await expect(card).toContainText('resets in 0:01')
  await expect(card).not.toContainText('resets in 0:02')
  await native.page.clock.fastForward(60_000)
  await expect(card).not.toContainText('resets in')
  await expect(card).toContainText('92% used')
})
