import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { COMMAND_CODE_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

commandCodeTest('uses the selected alternate native model before and after reload', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseNativeOption(context, { groupId: 'model', value: COMMAND_CODE_ALT_MODEL_ID, nativeProof: request => expect(request.body).toMatchObject({ model: 'command-code-e2e-alt' }) })
})
