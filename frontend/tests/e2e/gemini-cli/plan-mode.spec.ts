import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

geminiTest('reads real file context through native plan mode after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseNativeReadOnlyPlan(context, {
    preparePlan: async () => {
      await chooseSettingsOption(page, 'permissionMode-plan')
      await waitForSettingsIdle(page)
    },
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('plan')
    },
  })
  await expect(assistantBubbles(page).filter({ hasText: 'Implement after the user selects execution mode.' })).toBeVisible()
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
})
