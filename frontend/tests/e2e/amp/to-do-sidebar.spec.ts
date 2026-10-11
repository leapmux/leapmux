import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { goalsAndTodosList } from '../helpers/goalsAndTodos'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { waitForSettingsHydrated } from '../helpers/ui'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('offers no native to-do tool and keeps an authoritative empty sidebar after real tool execution', async ({ native }, testInfo) => {
  const { page } = native
  const catalog = await readAmpExecutorCatalog(native, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
  expect(catalog.tools).toContain('shell_command')
  expect(catalog.tools.filter(name => /todo|update_plan|plan_update/i.test(name))).toEqual([])
  await exerciseShellToolExecution(native, { includeFailure: false })
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page, 'permissionMode')
    }
    const snapshot = await readNativeSidebarSnapshot(native)
    expect(snapshot.todos).toEqual([])
    await expect(goalsAndTodosList(page)).toHaveCount(0)
  }
})
