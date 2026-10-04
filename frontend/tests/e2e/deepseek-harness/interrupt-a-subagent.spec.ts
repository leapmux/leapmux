import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { tabById } from '../helpers/ui'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { nativeContext, runningChild } from './scenarios'

deepseekHarnessTest('interrupts only the actual continuable child and leaves the parent able to accept a prompt', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await openChildTabFromRow(page, child.row)
    const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(child.row).toHaveAttribute('data-status', 'interrupted')
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    await expect(interrupt).toHaveCount(0)
    await waitForDeepseekHarnessChildReport(context, child.childId, child.parentId)
    await expect(page.locator('[data-testid="notification-divider"]:visible').filter({ hasText: /^Subagent (?:completed|failed|stopped|interrupted)$/ })).toHaveCount(0)
    await tabById(page, child.parentId).click()
    await sendNativeAnswer(context, 'Continue the parent after the actual child interruption.', 'The native parent remained available.')
  }, child.finish)
})
