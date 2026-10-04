import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseDeepseekHarnessCompaction } from './compactionScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('accepts an empty-history compact command and then replaces actual model context through native compaction', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await sendMessage(page, '/compact')
  await waitForAgentIdle(page)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  expect((await modelScript.status()).requests).toHaveLength(0)
  await exerciseDeepseekHarnessCompaction(context)
})
