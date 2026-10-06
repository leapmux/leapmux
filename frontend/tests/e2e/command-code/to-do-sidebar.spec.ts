import { commandCodeTest, expect } from '../command-code-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeTaskCreateToolCall, commandCodeTaskUpdateToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'

commandCodeTest('creates and completes the actual native task and preserves its sidebar status', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue({ toolCalls: [commandCodeTaskCreateToolCall('native-task-create', 'Inspect the native checklist', 'Read the actual source and report its state.')] }, { text: 'The native task creation completed.' })
  await sendMessage(page, modelScript.prompt('Create the supplied native task.'))
  await waitForNativeToolSteps(native, start + 2)
  const created = nativeToolResult(await modelScript.requestAt(start + 1), 'native-task-create')
  const taskID = /^Task #(\d+) created:/m.exec(created)?.[1]
  if (!taskID)
    throw new Error('The native task creation returned no actual task ID.')
  await expandGoalsAndTodosSection(page)
  const item = goalsAndTodosList(page)
  await expect(item).toContainText('Inspect the native checklist')
  await expect(item.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
  const next = await modelScript.queue({ toolCalls: [commandCodeTaskUpdateToolCall('native-task-complete', taskID, 'completed')] }, { text: 'The native task completed.' })
  await sendMessage(page, modelScript.prompt('Complete the actual native task ID.'))
  await waitForNativeToolSteps(native, next + 2)
  expect(nativeToolResult(await modelScript.requestAt(next + 1), 'native-task-complete')).toContain('Status: pending -> completed')
  await expect(item.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(item.locator('[data-task-checkbox="completed"]')).toHaveCount(1)
})
