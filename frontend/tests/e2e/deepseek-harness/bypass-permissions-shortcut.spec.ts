import { expect } from '@playwright/test'
import { DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../../src/generated/contracts/deepseek-harness-protocol'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { nativeContext } from './scenarios'

deepseekHarnessTest('selects the native full-access preset and preserves it after reload', async ({ askingDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDeepseekHarnessWorkspace.workspaceId })
  await exerciseBypassPermissions(context, { settingsProof: agent => expect(nativeOptionValue(agent, DEEPSEEK_HARNESS_OPTION.Permissions)).toBe(DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess) })
})
