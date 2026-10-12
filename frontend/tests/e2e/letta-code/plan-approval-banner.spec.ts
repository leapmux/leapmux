import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { lettaTest } from '../letta-fixtures'

lettaTest('offers native tools without a dedicated plan approval route', async ({ native }) => {
  await expectNoPlanReview(native, { relatedProof: async () => {
    const request = await sendNativeAnswer(native, 'Record the actual native approval tool catalog.', 'The native approval catalog probe completed.')
    expect(nativeModelToolNames(request)).not.toContain('ExitPlanMode')
    expect(nativeModelToolNames(request)).not.toContain('EnterPlanMode')
    await exerciseShellToolExecution(native, { includeFailure: false })
  } })
})
