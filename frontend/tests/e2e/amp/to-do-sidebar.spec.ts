import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { waitForSettingsHydrated } from '../helpers/ui'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('offers no native to-do tool and keeps an authoritative empty sidebar after real tool execution', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, testInfo) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  const catalog = await readAmpExecutorCatalog(context, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
  expect(catalog.tools).toContain('shell_command')
  expect(catalog.tools.filter(name => /todo|update_plan|plan_update/i.test(name))).toEqual([])
  await exerciseShellToolExecution(context, { includeFailure: false })
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page, 'permissionMode')
    }
    const snapshot = await readNativeSidebarSnapshot(context)
    expect(snapshot.todos).toEqual([])
    await expect(page.locator('[data-testid="goals-and-todos"]:visible')).toHaveCount(0)
  }
})
