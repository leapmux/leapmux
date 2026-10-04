import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeContext } from './scenarios'

deepseekHarnessTest('selects the native full-access preset and preserves it after reload', async ({ defaultDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: defaultDeepseekHarnessWorkspace.workspaceId })
  await exerciseBypassPermissions(context, { settingsProof: agent => expect(agent.optionGroups.find(group => group.id === 'permissions')?.currentValue).toBe('danger-full-access') })
})
