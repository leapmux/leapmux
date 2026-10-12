import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { openChildTabFromRow, stopChildWithInterrupt, subagentEndDivider } from '../helpers/subagentRegistry'
import { tabById } from '../helpers/ui'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { runningChild } from './scenarios'

deepseekHarnessTest('interrupts only the actual continuable child and leaves the parent able to accept a prompt', async ({ native }) => {
  const { page } = native
  const child = await runningChild(native)
  await withCleanup(async () => {
    await openChildTabFromRow(page, child.row)
    await stopChildWithInterrupt(page, child.row, 'interrupted')
    await waitForDeepseekHarnessChildReport(native, child.childId, child.parentId)
    await expect(subagentEndDivider(page)).toHaveCount(0)
    await tabById(page, child.parentId).click()
    await sendNativeAnswer(native, 'Continue the parent after the actual child interruption.', 'The native parent remained available.')
  }, child.finish)
})
