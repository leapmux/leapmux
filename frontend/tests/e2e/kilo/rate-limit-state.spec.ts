import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { kiloTest } from '../kilo-fixtures'

kiloTest('receives actual quota-bearing model headers without publishing quota UI', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await expectNoRateLimitState(context, {
    relatedProof: async () => {
      const start = (await modelScript.status()).stepCount
      const answer = 'The actual quota-bearing native response completed.'
      await modelScript.queue({ text: answer, rateLimits: { type: 'five_hour', status: 'allowed_warning', utilization: 0.92, resetsAt: Math.floor(Date.now() / 1000) + 3600 } })
      await sendMessage(page, modelScript.prompt('Complete the native quota header scenario.'))
      await modelScript.waitForSteps(start + 1)
      await waitForAgentIdle(page)
      const request = (await modelScript.status()).requests.find(record => record.stepIndex === start)
      expect(request?.response?.status).toBe(200)
      expect(request?.response?.headers['anthropic-ratelimit-unified-status']).toBe('allowed_warning')
      expect(request?.response?.headers['anthropic-ratelimit-unified-5h-utilization']).toBe('0.92')
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    },
  })
})
