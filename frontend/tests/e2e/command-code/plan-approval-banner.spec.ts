import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

commandCodeTest('returns a native read-only plan without a plan approval dialog', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await expectNoNativeControl(context, { testId: 'control-banner', additionalTestIds: ['plan-approve-btn', 'plan-reject-btn'], relatedProof: async () => {
    await exerciseNativeReadOnlyPlan(context, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'permissionMode-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: request => expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i),
    })
  } })
})
