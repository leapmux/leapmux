import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { exerciseGoosePlanLimit } from './planLimitScenario'

gooseTest('offers native Chat and execution modes without a Plan mode', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseGoosePlanLimit(context)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Chat')
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.options.map(option => option.id)).not.toContain('plan')
})
