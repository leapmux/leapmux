import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { currentNativeAgent, nativeOptionGroup } from '../helpers/nativeScenario'
import { expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { exerciseGoosePlanLimit } from './planLimitScenario'

gooseTest('offers native Chat and execution modes without a Plan mode', async ({ native }) => {
  const { page } = native
  await exerciseGoosePlanLimit(native)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Chat')
  expect(nativeOptionGroup(await currentNativeAgent(native), 'permissionMode')?.options.map(option => option.id)).not.toContain('plan')
})
