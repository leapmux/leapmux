import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { pickNumber } from '../../../src/lib/jsonPick'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('shows Kiro\'s native context percentage in the agent info card', async ({ authenticatedKiroWorkspace, leapmuxServer, page, modelScript }) => {
    void authenticatedKiroWorkspace
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first().getAttribute('data-tab-id') ?? ''
    expect(agentId).not.toBe('')
    const watch = await watchAgentContextUsage(leapmuxServer, agentId)
    try {
      const nativePercentages = () => watch.readings()
        .map(reading => pickNumber(reading, CONTEXT_USAGE_FIELD.UsagePercent))
        .filter((value): value is number => value !== null)
      const beforeCount = nativePercentages().length
      await modelScript.queue({ text: 'Usage recorded.', usage: { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 } })
      await sendMessage(page, modelScript.prompt('Reply once.'))
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)
      await expect.poll(() => nativePercentages().length).toBeGreaterThan(beforeCount)
      const percentage = nativePercentages().at(-1)!
      expect(percentage).toBeGreaterThanOrEqual(0)
      expect(percentage).toBeLessThanOrEqual(100)
      const card = await openAgentInfoCard(page)
      await expect(card).toContainText(`${Math.round(percentage)}% of the context window`)
    }
    finally {
      watch.cancel()
    }
  })
})
