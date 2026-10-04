import { expect } from '@playwright/test'
import { deepseekHarnessContextUsage } from '../../../src/components/chat/providers/deepseekharness/sessionMetadata'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

deepseekHarnessTest('shows actual native nonzero context use', async ({ deepseekHarnessWorkspace, page, modelScript }) => {
  void deepseekHarnessWorkspace
  await exerciseContextUsage(page, modelScript)
})

deepseekHarnessTest('preserves explicit zero native usage in the Context row after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  await modelScript.queue({ text: 'The actual native zero-count turn completed.', usage: { inputTokens: 0, outputTokens: 0 } })
  await sendMessage(page, modelScript.prompt('Complete the exact native zero-count turn.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  for (const reloaded of [false, true]) {
    if (reloaded)
      await page.reload()
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const usage = snapshot.messages.map(message => deepseekHarnessContextUsage(parseMessageContent(message)))
    expect(usage).toContainEqual({ inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, contextTokens: 0 })
    const card = await openAgentInfoCard(page)
    const row = card.getByText('Context', { exact: true }).locator('..')
    await expect(row).toContainText('0 / 1M')
  }
})
