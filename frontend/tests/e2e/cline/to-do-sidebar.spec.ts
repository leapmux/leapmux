import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { waitForSettingsHydrated } from '../helpers/ui'

clineTest('offers no native to-do tool and keeps an authoritative empty sidebar after real tool execution', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseShellToolExecution(context, { includeFailure: false })
  const request = (await modelScript.status()).requests.find(value => value.stepIndex === 0)
  if (!request)
    throw new Error('The native Cline shell turn produced no model catalog.')
  const tools = nativeModelToolNames(request)
  expect(tools).toContain('run_commands')
  expect(tools.filter(name => /todo|update_plan|plan_update/i.test(name))).toEqual([])
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    const snapshot = await readNativeSidebarSnapshot(context)
    expect(snapshot.todos).toEqual([])
    await expect(page.locator('[data-testid="goals-and-todos"]:visible')).toHaveCount(0)
  }
})
