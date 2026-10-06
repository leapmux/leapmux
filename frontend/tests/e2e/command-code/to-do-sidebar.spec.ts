import { commandCodeTest, expect } from '../command-code-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeTaskCreateToolCall, commandCodeTaskUpdateToolCall } from '../helpers/providerToolCalls'

commandCodeTest('creates and completes the actual native task and preserves its sidebar status', async ({ native }) => {
  const { page } = native
  const creation = await runNativeToolTurn(native, {
    toolCalls: [commandCodeTaskCreateToolCall('native-task-create', 'Inspect the native checklist', 'Read the actual source and report its state.')],
    prompt: 'Create the supplied native task.',
    answer: 'The native task creation completed.',
  })
  const created = nativeToolResult(creation.resultRequest, 'native-task-create')
  const taskID = /^Task #(\d+) created:/m.exec(created)?.[1]
  if (!taskID)
    throw new Error('The native task creation returned no actual task ID.')
  await expandGoalsAndTodosSection(page)
  const item = goalsAndTodosList(page)
  await expect(item).toContainText('Inspect the native checklist')
  await expect(item.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
  const completion = await runNativeToolTurn(native, {
    toolCalls: [commandCodeTaskUpdateToolCall('native-task-complete', taskID, 'completed')],
    prompt: 'Complete the actual native task ID.',
    answer: 'The native task completed.',
  })
  expect(nativeToolResult(completion.resultRequest, 'native-task-complete')).toContain('Status: pending -> completed')
  await expect(item.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(item.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
})
