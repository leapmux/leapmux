import { expect } from '@playwright/test'
import { TodoStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { updateTodosToolCall } from '../helpers/providerToolCalls'

deepseekHarnessTest('updates the whole native to-do snapshot and keeps sidebar status after reload', async ({ native }) => {
  const { page } = native
  const creation = await runNativeToolTurn(native, {
    toolCalls: [updateTodosToolCall(native.provider, 'native-todo-create', [{ step: 'Inspect the native source', status: 'in_progress' }, { step: 'Report the native result', status: 'pending' }])],
    prompt: 'Create the exact native to-do snapshot.',
    answer: 'The native to-do snapshot completed.',
  })
  expect(nativeToolResult(creation.resultRequest, 'native-todo-create')).toContain('Updated todo list: 1 pending, 1 in progress, 0 completed.')
  const initial = await readNativeSidebarSnapshot(native)
  expect(initial.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual([
    { content: 'Inspect the native source', status: TodoStatus.IN_PROGRESS },
    { content: 'Report the native result', status: TodoStatus.PENDING },
  ])
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list).toContainText('Inspect the native source')
  await expect(list).toContainText('Report the native result')
  await expect(list.locator('[data-task-checkbox="in_progress"]')).toHaveCount(1)
  await runNativeToolTurn(native, {
    toolCalls: [updateTodosToolCall(native.provider, 'native-todo-complete', [{ step: 'Inspect the native source', status: 'completed' }, { step: 'Report the native result', status: 'completed' }])],
    prompt: 'Complete both actual native to-do entries.',
    answer: 'The native to-do work completed.',
  })
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  const completed = await readNativeSidebarSnapshot(native)
  const expected = [
    { content: 'Inspect the native source', status: TodoStatus.COMPLETED },
    { content: 'Report the native result', status: TodoStatus.COMPLETED },
  ]
  expect(completed.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual(expected)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  const restored = await readNativeSidebarSnapshot(native)
  expect(restored.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual(expected)
})
