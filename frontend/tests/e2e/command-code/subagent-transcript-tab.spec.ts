import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents, userBubbles } from '../helpers/ui'
import { runningChild } from './scenarios'

commandCodeTest('preserves the actual native child prompt and final report in its own tab', async ({ native }) => {
  const { page } = native
  const child = await runningChild(native)
  await child.finish()
  await openChildTabFromRow(page, child.row)
  await expect(userBubbles(page).filter({ hasText: 'COMMANDCODECHILD' }).first()).toBeVisible()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
  await page.reload()
  await expect(userBubbles(page).filter({ hasText: 'COMMANDCODECHILD' }).first()).toBeVisible()
  await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' }).first()).toBeVisible()
})
