import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, tabById, userBubbles } from '../helpers/ui'
import { openCursorRunningChild } from './childScenario'

cursorTest('shows an actual child Read result in its own tab before the child completes', async ({ native }) => {
  const { page } = native
  const child = await openCursorRunningChild(native)
  await withCleanup(async () => {
    await openChildTabFromRow(page, child.row)
    await expect(userBubbles(page).filter({ hasText: child.prompt }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: child.marker }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: child.answer })).toHaveCount(0)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await tabById(page, child.parentId).click()
    await expect(messageContents(page).filter({ hasText: child.marker })).toHaveCount(0)
    await tabById(page, child.childId).click()
  }, child.finish)
  await tabById(page, child.childId).click()
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
  await page.reload()
  await expect(messageContents(page).filter({ hasText: child.marker }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
})
