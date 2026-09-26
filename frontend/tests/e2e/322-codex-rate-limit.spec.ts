import { codexTest } from './codex-fixtures'
import { expectRateLimitWindow } from './helpers/rateLimit'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex rate-limit state', () => {
  // Codex parses the `x-codex-*` response headers and publishes
  // `account/rateLimits/updated`. The mock writes those headers from the step's
  // `rateLimits` field, so the popover row is proof that the CLI forwarded the
  // status and that LeapMux stored it.
  codexTest('the agent info card shows the rate-limit window the model reported', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
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

  // A `seven_day` step rides Codex's secondary window. The popover labels that
  // window "7-Day Rate Limit", so the heading proves which tier arrived.
  codexTest('the agent info card shows the weekly window when the model reports one', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const rateLimits = {
      type: 'seven_day',
      status: 'allowed_warning',
      utilization: 0.81,
      resetsAt: Math.floor(Date.now() / 1000) + 86400,
    }
    await modelScript.queue({ text: 'Weekly window recorded.', rateLimits })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectRateLimitWindow(page, rateLimits)
  })
})
