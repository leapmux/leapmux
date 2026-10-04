import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection } from '../helpers/subagentRegistry'
import { junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview } from './planScenarios'
import { nativeContext } from './scenarios'

junieTest('keeps native delivery-plan tasks and their status after reload', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseNativePlanReview(context)
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible').first()
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(2)
  const states = await list.locator('[data-task-checkbox]').evaluateAll(elements => elements.map(element => element.getAttribute('data-task-checkbox')))
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Apply the change')
  expect(await list.locator('[data-task-checkbox]').evaluateAll(elements => elements.map(element => element.getAttribute('data-task-checkbox')))).toEqual(states)
})
