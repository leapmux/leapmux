import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseDeepseekHarnessCompaction } from './compactionScenarios'

deepseekHarnessTest('accepts an empty-history compact command and then replaces actual model context through native compaction', async ({ native }) => {
  const { page, modelScript } = native
  await sendMessage(page, '/compact')
  await waitForAgentIdle(page)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  // The empty history gives the native compactor nothing to send, so no model request arrives.
  expect((await modelScript.status()).requests).toHaveLength(0)
  await exerciseDeepseekHarnessCompaction(native)
})
