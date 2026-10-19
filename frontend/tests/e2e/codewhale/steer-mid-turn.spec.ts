import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseSteerBeforeTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { applyPermissionPreset, messageContents, waitForSettingsHydrated } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('steers a queued message into the active turn', async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await exerciseSteerBeforeTool(native)
  })

  codewhaleTest('shows one steer row when a steer arrives during streamed text', async ({ native }) => {
    await exerciseSteerDuringText(native, {
      answer: 'continue',
      checkTranscript: async () => {
        await expect(messageContents(native.page).filter({ hasText: 'Steer input queued' })).toHaveCount(0)
      },
    })
  })
})
