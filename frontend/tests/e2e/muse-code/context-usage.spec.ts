import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { withCleanup } from '../helpers/cleanup'
import { exerciseContextUsage, readContextRow } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('shows native token counts and preserves an actual zero update', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchAgentContextUsage(native.leapmuxServer, agent.id)
  await withCleanup(async () => {
    const usage = await exerciseContextUsage(native)
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: usage.inputTokens,
      [CONTEXT_USAGE_FIELD.OutputTokens]: usage.outputTokens,
    })
    const before = watch.readings().length
    const step = await native.modelScript.queue({ text: 'The zero usage turn completed.', usage: { inputTokens: 0, outputTokens: 0 } })
    await sendMessage(native.page, native.modelScript.prompt('Reply once with the scripted zero usage response.'))
    await native.modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(native.page)
    await expect.poll(() => watch.readings().length).toBeGreaterThan(before)
    await expect.poll(() => watch.readings().at(-1)).toMatchObject({
      [CONTEXT_USAGE_FIELD.InputTokens]: 0,
      [CONTEXT_USAGE_FIELD.OutputTokens]: 0,
    })
    await expect.poll(() => readContextRow(native.page)).toMatchObject({ tokens: 0 })
    expect((await native.modelScript.requestAt(step)).mockCredential?.accepted).toBe(true)
  }, async () => watch.cancel())
})
