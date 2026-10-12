import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { withCleanup } from '../helpers/cleanup'
import { exerciseContextUsage } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { museTest } from '../muse-fixtures'

// Muse counts its own occupancy from the provider-reported facts: a model reply
// that states zero usage still leaves the host's own accounting above zero, so no
// zero reading exists to preserve. The count the host reports is the truth drawn.
museTest('shows the native token counts the host accounts', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchAgentContextUsage(native.leapmuxServer, agent.id)
  await withCleanup(async () => {
    const usage = await exerciseContextUsage(native)
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: usage.inputTokens,
      [CONTEXT_USAGE_FIELD.OutputTokens]: usage.outputTokens,
    })
    // A later turn with a different usage moves the count the host reports.
    const second = await exerciseContextUsage(native)
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: second.inputTokens,
      [CONTEXT_USAGE_FIELD.OutputTokens]: second.outputTokens,
    })
  }, async () => watch.cancel())
})
