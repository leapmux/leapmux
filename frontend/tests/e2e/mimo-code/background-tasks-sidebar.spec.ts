import { expect } from '@playwright/test'
import { backgroundTasksSection, expectRowBecomesFinal, expectSectionPersists, HELD_CHILD_TASK, HELD_CHILD_TITLE, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset, tabById } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest('keeps the actual native background task row through completion and reload', async ({ page, native: context }) => {
  await applyPermissionPreset(page, 'bypass')
  const child = await openHeldChildTab(context, { childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
  try {
    await tabById(page, child.parentId).click()
    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await expect(child.row).toContainText(HELD_CHILD_TITLE)
  }
  finally {
    await child.finish()
  }
  await expectRowBecomesFinal(page, child.row)
  await expectSectionPersists(page)
})
