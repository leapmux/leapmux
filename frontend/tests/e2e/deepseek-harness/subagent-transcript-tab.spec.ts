import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expandNativeResultView, nativeResultBubble } from '../helpers/nativeResultView'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents, tabById, userBubbles } from '../helpers/ui'
import { nativeContext, runningChild } from './scenarios'

deepseekHarnessTest('opens a separate native child tab and keeps its own messages after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
    await expect(userBubbles(page).filter({ hasText: 'DEEPSEEKCHILD' }).first()).toBeVisible()
    // The native Read result starts with three header rows: `<path>`, `<type>`, and `<content>`. A result view shows only its first three rows until the reader expands it.
    await expandNativeResultView(nativeResultBubble(page, child.readCallId))
    await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_FILE' }).first()).toBeVisible()
    await tabById(page, child.parentId).click()
    await expect(userBubbles(page).filter({ hasText: 'DEEPSEEKCHILD' })).toHaveCount(0)
  }, child.finish)
  await tabById(page, child.childId).click()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
  await page.reload()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
})
