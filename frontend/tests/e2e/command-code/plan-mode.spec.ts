import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

commandCodeTest('reads actual file context in the native planning mode', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseNativeReadOnlyPlan(context, {
    preparePlan: async () => {
      await chooseSettingsOption(page, 'permissionMode-plan')
      await waitForSettingsIdle(page)
    },
    nativeProof: request => expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i),
  })
})
