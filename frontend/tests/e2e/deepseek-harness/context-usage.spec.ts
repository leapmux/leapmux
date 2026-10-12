import { expect } from '@playwright/test'
import { deepseekHarnessContextUsage } from '../../../src/components/chat/providers/deepseekharness/sessionMetadata'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseContextUsage, parseContextRow } from '../helpers/contextUsage'
import { DEEPSEEK_HARNESS_CONTEXT_WINDOW } from '../helpers/deepseekHarnessEnvironment'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

deepseekHarnessTest('shows actual native nonzero context use', async ({ native }) => {
  await exerciseContextUsage(native)
})

deepseekHarnessTest('preserves explicit zero native usage in the Context row after reload', async ({ native, page, modelScript }) => {
  const agent = await currentNativeAgent(native)
  const step = await modelScript.queue({ text: 'The actual native zero-count turn completed.', usage: { inputTokens: 0, outputTokens: 0 } })
  await sendMessage(page, modelScript.prompt('Complete the exact native zero-count turn.'))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  for (const reloaded of [false, true]) {
    if (reloaded)
      await page.reload()
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const usage = snapshot.messages.map(message => deepseekHarnessContextUsage(parseMessageContent(message)))
    // The Worker states the native context window (`request/context`) beside the native usage, which states none.
    expect(usage).toContainEqual({ inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, contextTokens: 0, contextWindow: DEEPSEEK_HARNESS_CONTEXT_WINDOW })
    const card = await openAgentInfoCard(page)
    const row = card.getByText('Context', { exact: true }).locator('..')
    // The card prints counts through `formatTokenCount`, so the 1,000,000 window reads "1.0M". The default window of 200,000 would read "200.0k".
    await expect.poll(async () => parseContextRow(await row.textContent() ?? '')).toEqual({ tokens: 0, window: DEEPSEEK_HARNESS_CONTEXT_WINDOW })
  }
})
