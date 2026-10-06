import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents, userBubbles } from '../helpers/ui'
import { nativeContext, runningChild } from './scenarios'

commandCodeTest('preserves the actual native child prompt and final report in its own tab', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  const child = await runningChild(context)
  await child.finish()
  await openChildTabFromRow(page, child.row)
  await expect(userBubbles(page).filter({ hasText: 'COMMANDCODECHILD' }).first()).toBeVisible()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
  await page.reload()
  await expect(userBubbles(page).filter({ hasText: 'COMMANDCODECHILD' }).first()).toBeVisible()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
})
