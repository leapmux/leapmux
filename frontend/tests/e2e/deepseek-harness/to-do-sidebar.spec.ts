import { expect } from '@playwright/test'
import { TodoStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

deepseekHarnessTest('updates the whole native to-do snapshot and keeps sidebar status after reload', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const start = (await modelScript.status()).stepCount
  await modelScript.queue(
    { toolCalls: [updateTodosToolCall(context.provider, 'native-todo-create', [{ step: 'Inspect the native source', status: 'in_progress' }, { step: 'Report the native result', status: 'pending' }])] },
    { text: 'The native to-do snapshot completed.' },
  )
  await sendMessage(page, modelScript.prompt('Create the exact native to-do snapshot.'))
  await waitForNativeToolSteps(context, start + 2)
  expect(nativeToolResult((await modelScript.status()).requests.find(request => request.stepIndex === start + 1), 'native-todo-create')).toContain('Updated todo list: 1 pending, 1 in progress, 0 completed.')
  const initial = await readNativeSidebarSnapshot(context)
  expect(initial.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual([
    { content: 'Inspect the native source', status: TodoStatus.IN_PROGRESS },
    { content: 'Report the native result', status: TodoStatus.PENDING },
  ])
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list).toContainText('Inspect the native source')
  await expect(list).toContainText('Report the native result')
  await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)
  const next = (await modelScript.status()).stepCount
  await modelScript.queue(
    { toolCalls: [updateTodosToolCall(context.provider, 'native-todo-complete', [{ step: 'Inspect the native source', status: 'completed' }, { step: 'Report the native result', status: 'completed' }])] },
    { text: 'The native to-do work completed.' },
  )
  await sendMessage(page, modelScript.prompt('Complete both actual native to-do entries.'))
  await waitForNativeToolSteps(context, next + 2)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  const completed = await readNativeSidebarSnapshot(context)
  const expected = [
    { content: 'Inspect the native source', status: TodoStatus.COMPLETED },
    { content: 'Report the native result', status: TodoStatus.COMPLETED },
  ]
  expect(completed.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual(expected)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  const restored = await readNativeSidebarSnapshot(context)
  expect(restored.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual(expected)
})
