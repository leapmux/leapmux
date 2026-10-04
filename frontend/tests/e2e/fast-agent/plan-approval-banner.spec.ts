import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

fastAgentTest('offers native tools without a dedicated plan approval route', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: async () => {
    const request = await sendNativeAnswer(context, 'Record the actual native approval tool catalog.', 'The native approval catalog probe completed.')
    expect(nativeModelToolNames(request)).not.toContain('ExitPlanMode')
    expect(nativeModelToolNames(request)).not.toContain('EnterPlanMode')
    await exerciseShellToolExecution(context, { includeFailure: false })
  } })
})
