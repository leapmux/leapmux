import { expect } from '@playwright/test'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { pickNumber } from '../../../src/lib/jsonPick'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('shows the current tokens from its native usage event', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedCopilotWorkspace
  const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
  expect(agentId).not.toBe('')
  const watch = await watchAgentContextUsage(leapmuxServer, agentId)
  try {
    const nativeTokenReadings = () => watch.readings().filter((reading) => {
      const tokens = pickNumber(reading, CONTEXT_USAGE_FIELD.ContextTokens)
      return tokens !== null && tokens > 0
    })
    const before = pickNumber(nativeTokenReadings().at(-1), CONTEXT_USAGE_FIELD.ContextTokens) ?? 0
    const beforeCount = nativeTokenReadings().length
    await modelScript.queue({ text: 'Usage recorded.' })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect.poll(() => nativeTokenReadings().length).toBeGreaterThan(beforeCount)
    const usage = nativeTokenReadings().at(-1)
    const currentTokens = pickNumber(usage, CONTEXT_USAGE_FIELD.ContextTokens)
    if (currentTokens === null)
      throw new Error('The native Copilot usage update has no token count.')
    expect(currentTokens).toBeGreaterThan(before)
    const popover = await openAgentInfoCard(page)
    await expect(popover).toContainText(formatTokenCount(currentTokens))
    const tokenLimit = pickNumber(usage, CONTEXT_USAGE_FIELD.ContextWindow)
    if (tokenLimit !== null)
      await expect(popover).toContainText(formatTokenCount(tokenLimit))
  }
  finally {
    watch.cancel()
  }
})
