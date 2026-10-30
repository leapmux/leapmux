import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { withCleanup } from '../helpers/cleanup'
import { exerciseContextUsage, readContextRow } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { sendMessage, visibleOnly } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})

// The mock reports a cache-INCLUSIVE input with the cache buckets inside it. The
// worker's projection must subtract each bucket out of `input_tokens`, so a
// token of cache traffic counts once, in its own bucket, and not also inside the
// input. The chat-completions wire carries the cache-read count only: the
// fixture's cache-write count reaches the mock's response but not this client's
// usage. The unit tests cover the cache-write projection.
zcodeTest('counts cache-inclusive input without double counting the cache buckets', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchAgentContextUsage(native.leapmuxServer, agent.id)
  await withCleanup(async () => {
    await exerciseContextUsage(native, {
      usage: {
        inputTokens: 10_000,
        cacheReadTokens: 2_000,
        cacheWriteTokens: 3_000,
        outputTokens: 500,
        contextWindow: 128_000,
      },
    })
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: 8_000,
      [CONTEXT_USAGE_FIELD.CacheReadInputTokens]: 2_000,
      [CONTEXT_USAGE_FIELD.CacheCreationInputTokens]: 0,
      // The authoritative context size keeps counting the whole request once.
      [CONTEXT_USAGE_FIELD.ContextTokens]: 10_500,
    })
  }, async () => watch.cancel())
})

zcodeTest('clears old native usage before another request and after reload', async ({ native }) => {
  await exerciseContextUsage(native)
  const before = await currentNativeAgent(native)
  await sendMessage(native.page, '/clear')
  await expect(visibleOnly(native.page.getByText('Context cleared', { exact: true })).first()).toBeVisible()
  await retryUntilPass(async () => {
    const current = await nativeAgentById(native, before.id)
    expect(current?.agentSessionId ?? '').not.toBe('')
    expect(current?.agentSessionId).not.toBe(before.agentSessionId)
  })
  expect((await readContextRow(native.page))?.tokens ?? 0).toBe(0)
  await native.page.reload()
  expect((await readContextRow(native.page))?.tokens ?? 0).toBe(0)
  await exerciseContextUsage(native, {
    usage: { inputTokens: 24_000, outputTokens: 80 },
  })
})
