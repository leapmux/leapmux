import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses the selected native reasoning effort before and after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ thinking: { type: 'enabled' }, output_config: { effort: 'low' } }) })
})
