import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { DEEPSEEK_HARNESS_ALT_MODEL_ID } from '../helpers/deepseekHarnessEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses the selected native model before and after reload', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseNativeOption(context, { groupId: 'model', value: DEEPSEEK_HARNESS_ALT_MODEL_ID, nativeProof: request => expect(request.body).toMatchObject({ model: 'deepseek-v4-pro' }) })
})
