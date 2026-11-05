import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { goalsAndTodosList } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'

antigravityTest('reports no native task list or to-do tool', async ({ native }) => {
  await sendNativeAnswer(native, 'List any native task tracker that the provider exposes.', 'Antigravity has no native to-do tool or task-list stream.')
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    expect((await readNativeSidebarSnapshot(native)).todos).toEqual([])
    await expect(goalsAndTodosList(native.page)).toHaveCount(0)
  }
})
