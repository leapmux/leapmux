import { codexTest } from '../codex-fixtures'
import { exerciseRateLimitWindow, nearLimitRateLimits } from '../helpers/rateLimit'

codexTest.describe('Codex rate-limit state', () => {
  // Codex parses the `x-codex-*` response headers and publishes
  // `account/rateLimits/updated`. The mock writes those headers from the step's
  // `rateLimits` field, so the popover row is proof that the CLI forwarded the
  // status and that LeapMux stored it.
  codexTest('the agent info card shows the rate-limit window the model reported', async ({ native }) => {
    await exerciseRateLimitWindow(native, nearLimitRateLimits())
  })

  // A `seven_day` step rides Codex's secondary window. The popover labels that
  // window "7-Day Rate Limit", so the heading proves which tier arrived.
  codexTest('the agent info card shows the weekly window when the model reports one', async ({ native }) => {
    await exerciseRateLimitWindow(native, {
      type: 'seven_day',
      status: 'allowed_warning',
      utilization: 0.81,
      resetsAt: Math.floor(Date.now() / 1000) + 86400,
    })
  })
})
