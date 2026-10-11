import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { pickNumber } from '../../../src/lib/jsonPick'
import { SCRIPTED_CONTEXT_USAGE } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('shows Kiro\'s native context percentage in the agent info card', async ({ native, leapmuxServer, page, modelScript }) => {
    const agentId = await selectedAgentTabId(native.page)
    const watch = await watchAgentContextUsage(leapmuxServer, agentId)
    try {
      const nativePercentages = () => watch.readings()
        .map(reading => pickNumber(reading, CONTEXT_USAGE_FIELD.UsagePercent))
        .filter((value): value is number => value !== null)
      const beforeCount = nativePercentages().length
      const step = await modelScript.queue({ text: 'Usage recorded.', usage: { ...SCRIPTED_CONTEXT_USAGE } })
      await sendMessage(page, modelScript.prompt('Reply once.'))
      await modelScript.waitForSteps(step + 1)
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
