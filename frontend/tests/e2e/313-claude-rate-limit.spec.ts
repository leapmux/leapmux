import { test } from './fixtures'
import { expectRateLimitWindow } from './helpers/rateLimit'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

test.describe('Claude Code rate-limit state', () => {
  // Claude Code reads the `anthropic-ratelimit-unified-*` response headers and
  // emits `rate_limit_event`. The mock writes those headers from the step's
  // `rateLimits` field, so the popover row is proof that the CLI forwarded the
  // status and that LeapMux stored it.
  test('the agent info card shows the rate-limit window the model reported', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    const rateLimits = {
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: Math.floor(Date.now() / 1000) + 3600,
    }
    await modelScript.queue({ text: 'Answered near the limit.', rateLimits })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectRateLimitWindow(page, rateLimits)
  })
})
