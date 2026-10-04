import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { nativeContext } from './scenarios'

deepseekHarnessTest('consumes a native request with quota headers without publishing an unsupported quota window', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await expectNoRateLimitState(context, { relatedProof: async () => {
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ text: 'The native quota header turn completed.', rateLimits: { type: 'five_hour', status: 'allowed_warning', utilization: 0.92, resetsAt: Math.floor(Date.now() / 1000) + 3600 } })
    await sendMessage(page, modelScript.prompt('Complete after the supplied native quota headers arrive.'))
    const status = await modelScript.waitForSteps(start + 1)
    expect(status.requests.find(request => request.stepIndex === start)?.protocol).toBe('anthropic-messages')
    await waitForAgentIdle(page)
  } })
})
