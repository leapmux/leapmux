import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { DEEPSEEK_HARNESS_ALT_MODEL_ID } from '../helpers/deepseekHarnessEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses the selected native reasoning effort before and after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ thinking: { type: 'enabled' }, output_config: { effort: 'low' } }) })
})

// Both models offer low. The second model starts at another default, so a reset shows.
deepseekHarnessTest('keeps the chosen effort after a model switch and a reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: DEEPSEEK_HARNESS_ALT_MODEL_ID,
    nativeProof: request => expect(request.body).toMatchObject({ model: 'deepseek-v4-pro', thinking: { type: 'enabled' }, output_config: { effort: 'low' } }),
  })
})
