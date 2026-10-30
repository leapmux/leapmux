import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { exerciseContextUsage } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { currentNativeAgent } from '../helpers/nativeScenario'

/**
 * The native usage event must reach the agent info card. The test checks the reported count and its display.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp basic chat', () => {
  ampTest('reports model usage in the agent info card', async ({ native }) => {
    await exerciseContextUsage(native, {
      usage: { inputTokens: 100, outputTokens: 40 },
    })
  })
  for (const [label, usage] of [
    [
      'omitted input',
      { outputTokens: 40 },
    ],
    [
      'cache buckets',
      {
        inputTokens: 10,
        cacheReadTokens: 2,
        cacheWriteTokens: 3,
        outputTokens: 4,
      },
    ],
    [
      'explicit zeros',
      { inputTokens: 0, outputTokens: 0 },
    ],
  ] as const) {
    ampTest(`reports context with ${label} before and after reload`, async ({ native }) => {
      const agent = await currentNativeAgent(native)
      const watch = await watchAgentContextUsage(native.leapmuxServer, agent.id)
      await withCleanup(async () => {
        await exerciseContextUsage(native, { usage })
        if (label === 'cache buckets') {
          await expect.poll(() => watch.readings().at(-1)).toMatchObject({
            [CONTEXT_USAGE_FIELD.InputTokens]: 5,
            [CONTEXT_USAGE_FIELD.CacheReadInputTokens]: 2,
            [CONTEXT_USAGE_FIELD.CacheCreationInputTokens]: 3,
            [CONTEXT_USAGE_FIELD.OutputTokens]: 4,
            [CONTEXT_USAGE_FIELD.ContextTokens]: 10,
          })
        }
      }, async () => watch.cancel())
    })
  }
})
