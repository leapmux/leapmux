import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { goalsAndTodosList } from '../helpers/goalsAndTodos'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { waitForSettingsHydrated } from '../helpers/ui'

clineTest('offers no native to-do tool and keeps an authoritative empty sidebar after real tool execution', async ({ native }) => {
  const { page, modelScript } = native
  await exerciseShellToolExecution(native, { includeFailure: false })
  // The shell scenario queues the first steps of this test, so its first model request is step 0.
  const tools = nativeModelToolNames(await modelScript.requestAt(0))
  expect(tools).toContain('run_commands')
  expect(tools.filter(name => /todo|update_plan|plan_update/i.test(name))).toEqual([])
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    const snapshot = await readNativeSidebarSnapshot(native)
    expect(snapshot.todos).toEqual([])
    await expect(goalsAndTodosList(page)).toHaveCount(0)
  }
})
