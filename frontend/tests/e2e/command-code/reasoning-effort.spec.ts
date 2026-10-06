import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

commandCodeTest('uses the selected native effort before and after reload', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ model: 'command-code-e2e', reasoning_effort: 'low' }) })
})
