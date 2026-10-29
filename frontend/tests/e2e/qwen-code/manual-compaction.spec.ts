import { expect } from '@playwright/test'
import { compactionSummaryText, exerciseNativeCompaction, seedManualCompactionConversation } from '../helpers/manualCompaction'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'
import { QWEN_COMPACTION } from './compactionScenario'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, QWEN_COMPACTION)
  })

  // A history too small to benefit states its own refusal: Qwen Code fails the
  // /compress prompt with its not-beneficial error, which becomes one status
  // row, and the failed prompt draws no "Turn failed" divider of its own.
  qwenTest('states its own refused compression as a status row, not a failed prompt', async ({ native }) => {
    const { page, modelScript } = native
    await seedManualCompactionConversation(native)
    await modelScript.fallback({
      text: compactionSummaryText(QWEN_COMPACTION),
    })
    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'Qwen Code did not compact the context' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'Turn failed' })).toHaveCount(0)
  })
})
