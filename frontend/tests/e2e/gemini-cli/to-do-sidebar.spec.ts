import { expect } from '@playwright/test'
import { TodoStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { geminiTodoSnapshotToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, expectRelatedTodoSurvivesReload } from '../helpers/relatedTodoProof'
import { expandGoalsAndTodosSection } from '../helpers/subagentRegistry'
import { applyPermissionPreset, sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

// Gemini CLI asks for approval of write_todos in its default mode: the tool is not
// in the allow list of its read-only policy (bundle/policies/read-only.toml). The
// shared proof answers no approval, so this case runs under the bypass shortcut
// (native yolo). The next case answers the approval in the default mode.
geminiTest('stores an exact native task snapshot and preserves it after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const item = 'Keep the native Gemini task'
  await exerciseRelatedTodo(context, {
    toolCall: updateTodosToolCall(context.provider, 'gemini-sidebar-todo', [{ step: item, status: 'pending' }]),
    item,
    prepare: () => applyPermissionPreset(page, 'bypass'),
  })
  await expectRelatedTodoSurvivesReload(context, item)
})

geminiTest('preserves all native task statuses and replaces and clears the saved snapshot', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const statuses = ['pending', 'in_progress', 'completed', 'cancelled', 'blocked'] as const
  const nativeTodos = statuses.map(status => ({ description: `GEMINI_NATIVE_TASK_${status}`, status }))
  const canonicalStatuses = {
    pending: { value: TodoStatus.PENDING, checkbox: 'pending' },
    in_progress: { value: TodoStatus.IN_PROGRESS, checkbox: 'in_progress' },
    completed: { value: TodoStatus.COMPLETED, checkbox: 'completed' },
    cancelled: { value: TodoStatus.DELETED, checkbox: 'deleted' },
    blocked: { value: TodoStatus.BLOCKED, checkbox: 'blocked' },
  } as const
  const replacement = [{ description: 'GEMINI_NATIVE_REPLACEMENT', status: 'blocked' }] as const
  const snapshots = [nativeTodos, replacement, []] as const
  for (const [index, todos] of snapshots.entries()) {
    const start = (await modelScript.status()).stepCount
    const callId = `gemini-native-todo-snapshot-${index}`
    await modelScript.queue({ toolCalls: [geminiTodoSnapshotToolCall(callId, todos)] }, { text: 'The native task snapshot completed.' })
    await sendMessage(page, modelScript.prompt('Replace the native task list with the scripted snapshot.'))
    await waitForNativeToolSteps(context, start + 2)
    const request = (await modelScript.status()).requests.find(row => row.stepIndex === start + 1)
    const returned = nativeToolResult(request, callId)
    expect(returned).toContain(todos.length === 0 ? 'Successfully cleared the todo list.' : 'Successfully updated the todo list.')
    for (const todo of todos)
      expect(returned).toContain(`[${todo.status}] ${todo.description}`)
    const verifySnapshot = async () => {
      const snapshot = await readNativeSidebarSnapshot(context)
      expect(snapshot.todos.map(todo => ({ content: todo.content, status: todo.status }))).toEqual(todos.map(todo => ({ content: todo.description, status: canonicalStatuses[todo.status].value })))
      const list = page.locator('[data-testid="goals-and-todos"]:visible').first()
      if (todos.length === 0) {
        await expect(list.locator('[data-task-checkbox]')).toHaveCount(0)
        return
      }
      await expandGoalsAndTodosSection(page)
      await expect(list.locator('[data-task-checkbox]')).toHaveCount(todos.length)
      for (const todo of todos) {
        await expect(list.getByText(todo.description, { exact: true })).toBeVisible()
        await expect(list.locator(`[data-task-checkbox="${canonicalStatuses[todo.status].checkbox}"]`)).toHaveCount(1)
      }
      if (index > 0) {
        for (const todo of nativeTodos)
          await expect(list.getByText(todo.description, { exact: true })).toHaveCount(0)
      }
    }
    await verifySnapshot()
    await page.reload()
    await verifySnapshot()
  }
})
