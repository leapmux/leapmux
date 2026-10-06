import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('follows native create, update, and clear snapshots across reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await modelScript.queue(
    { toolCalls: [piTodoToolCall('pi-todo-inspect', { action: 'create', subject: 'Inspect the repository' })] },
    { toolCalls: [piTodoToolCall('pi-todo-report', { action: 'create', subject: 'Report the finding' })] },
    { text: 'Both tasks are ready.' },
  )
  await sendMessage(page, modelScript.prompt('Create two to-do tasks.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Report the finding')
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(2)

  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(2)

  await modelScript.queue(
    { toolCalls: [piTodoToolCall('pi-todo-complete', { action: 'update', id: 1, status: 'completed' })] },
    { text: 'The first task is complete.' },
  )
  await sendMessage(page, modelScript.prompt('Complete the first to-do task.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)

  await modelScript.queue(
    { toolCalls: [piTodoToolCall('pi-todo-clear', { action: 'clear' })] },
    { text: 'The task list is clear.' },
  )
  await sendMessage(page, modelScript.prompt('Clear the to-do list.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(0)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(0)
})
