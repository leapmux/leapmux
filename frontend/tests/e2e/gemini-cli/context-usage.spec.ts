import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { exerciseContextUsage } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { currentNativeAgent } from '../helpers/nativeScenario'

geminiTest('uses the last native request counts through the context surface', async ({ native }) => {
  await exerciseContextUsage(native)
})

geminiTest('counts cache-only usage live and after reload', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchAgentContextUsage(native.leapmuxServer, agent.id)
  await withCleanup(async () => {
    await exerciseContextUsage(native, {
      usage: { cacheReadTokens: 3 },
    })
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: 0,
      [CONTEXT_USAGE_FIELD.CacheReadInputTokens]: 3,
      [CONTEXT_USAGE_FIELD.OutputTokens]: 1,
    })
  }, async () => watch.cancel())
})
