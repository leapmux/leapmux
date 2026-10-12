import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents, tabById, userBubbles } from '../helpers/ui'
import { runningChild } from './scenarios'

commandCodeTest('shows exact native child tool activity before the final report', async ({ native }) => {
  const { page } = native
  const child = await runningChild(native)
  await withCleanup(async () => {
    await openChildTabFromRow(page, child.row)
    await expect(userBubbles(page).filter({ hasText: 'COMMANDCODECHILD' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'read_file' }).filter({ hasText: 'native-child-' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_REPORT' })).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'NATIVE_CHILD_FILE' })).toHaveCount(0)
    await tabById(page, child.parentId).click()
    await expect(messageContents(page).filter({ hasText: 'read_file' }).filter({ hasText: 'native-child-' })).toHaveCount(0)
  }, child.finish)
})
