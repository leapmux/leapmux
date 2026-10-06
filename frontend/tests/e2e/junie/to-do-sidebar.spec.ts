import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview } from './planScenarios'

junieTest('keeps native delivery-plan tasks and their status after reload', async ({ native }) => {
  const { page } = native
  await exerciseNativePlanReview(native)
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(2)
  const states = await list.locator('[data-task-checkbox]').evaluateAll(elements => elements.map(element => element.getAttribute('data-task-checkbox')))
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Apply the change')
  expect(await list.locator('[data-task-checkbox]').evaluateAll(elements => elements.map(element => element.getAttribute('data-task-checkbox')))).toEqual(states)
})
