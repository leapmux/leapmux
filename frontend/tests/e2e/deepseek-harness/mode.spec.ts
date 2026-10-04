import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

deepseekHarnessTest('preserves the selected native plan mode before and after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseNativeOption(context, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: request => expect(nativeModelInstructionText(request)).toContain('You are in plan mode.'),
  })
})
